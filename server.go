package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type apiServer struct {
	app         *App
	log         *log.Logger
	prefetchMu  sync.Mutex
	prefetching map[string]struct{}
	prefetchSem chan struct{}
}

func newHTTPServer(app *App, logger *log.Logger) http.Handler {
	srv := &apiServer{app: app, log: logger, prefetching: map[string]struct{}{}, prefetchSem: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/bootstrap", srv.auth(srv.handleBootstrap))
	mux.HandleFunc("/api/v1/home", srv.auth(srv.handleHome))
	mux.HandleFunc("/api/v1/categories", srv.auth(srv.handleCategories))
	mux.HandleFunc("/api/v1/comics", srv.auth(srv.handleComics))
	mux.HandleFunc("/api/v1/search", srv.auth(srv.handleSearch))
	mux.HandleFunc("/api/v1/comics/", srv.auth(srv.handleComicSubtree))
	mux.HandleFunc("/api/v1/favorites/folders", srv.auth(srv.handleFavoriteFolders))
	mux.HandleFunc("/api/v1/favorites/folders/", srv.auth(srv.handleFavoriteFolderDelete))
	mux.HandleFunc("/api/v1/favorites/comics", srv.auth(srv.handleFavoriteComics))
	mux.HandleFunc("/api/v1/favorites/items", srv.auth(srv.handleFavoriteItems))
	mux.HandleFunc("/api/v1/admin/rescan", srv.auth(srv.handleRescan))
	mux.HandleFunc("/api/v1/admin/metadata/refresh", srv.auth(srv.handleMetadataRefresh))
	mux.HandleFunc("/media/", srv.handleMedia)
	mux.HandleFunc("/", srv.handleIndex)
	return loggingMiddleware(logger, mux)
}

func (s *apiServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(s.app.cfg.Server.Token)
		if token != "" {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if auth != "Bearer "+token {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid token")
				return
			}
		}
		next(w, r)
	}
}

func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func (s *apiServer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Venera Home Server\n")
}

func (s *apiServer) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	s.app.comicsMu.RLock()
	defer s.app.comicsMu.RUnlock()
	libraries := make([]map[string]any, 0, len(s.app.cfg.Libraries))
	for _, lib := range s.app.cfg.Libraries {
		libraries = append(libraries, map[string]any{
			"id":           lib.ID,
			"name":         lib.Name,
			"kind":         lib.Kind,
			"series_count": len(s.app.libraries[lib.ID]),
		})
	}
	writeData(w, map[string]any{
		"server":       map[string]any{"name": "Venera Home Server", "version": "0.1.0"},
		"capabilities": map[string]any{"favorites": true, "thumbnails": true, "rescan": true, "metadata_fetch": false},
		"libraries":    libraries,
		"defaults":     map[string]any{"sort": "updated_desc", "page_size": 24},
	})
}

func (s *apiServer) handleHome(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	libraryID := r.URL.Query().Get("library_id")
	sections := []map[string]any{}
	sections = append(sections, map[string]any{
		"id":        "recent_updated",
		"title":     "Recently Updated",
		"items":     s.toCards(base, take(s.filteredComics(libraryID, "all", "", "updated_desc"), 12), false),
		"view_more": map[string]any{"category": "all", "param": ""},
	})
	sections = append(sections, map[string]any{
		"id":        "recent_added",
		"title":     "Recently Added",
		"items":     s.toCards(base, take(s.filteredComics(libraryID, "all", "", "added_desc"), 12), false),
		"view_more": map[string]any{"category": "all", "param": ""},
	})
	sections = append(sections, map[string]any{
		"id":        "random",
		"title":     "Random",
		"items":     s.toCards(base, take(s.filteredComics(libraryID, "all", "", "random"), 12), false),
		"view_more": map[string]any{"category": "all", "param": ""},
	})
	writeData(w, map[string]any{"sections": sections})
}

func (s *apiServer) handleCategories(w http.ResponseWriter, r *http.Request) {
	a := s.app
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	libCounts := map[string]int{}
	tagCounts := map[string]int{}
	authorCounts := map[string]int{}
	storageCounts := map[string]int{}
	for _, comic := range a.comics {
		libCounts[comic.LibraryID]++
		storageCounts[comic.Storage]++
		for _, tag := range comic.Tags {
			tagCounts[tag]++
		}
		for _, author := range comic.Authors {
			authorCounts[author]++
		}
	}
	groups := []CategoryGroup{
		{Key: "library", Title: "Libraries", Items: makeItemsFromLibraries(a.cfg.Libraries, libCounts)},
		{Key: "tag", Title: "Tags", Items: makeItemsFromMap(tagCounts)},
		{Key: "author", Title: "Authors", Items: makeItemsFromMap(authorCounts)},
		{Key: "storage", Title: "Storage", Items: makeItemsFromMap(storageCounts)},
	}
	writeData(w, map[string]any{"groups": groups})
}

func makeItemsFromLibraries(libs []LibraryConfig, counts map[string]int) []CategoryItem {
	out := make([]CategoryItem, 0, len(libs))
	for _, lib := range libs {
		out = append(out, CategoryItem{ID: lib.ID, Label: lib.Name, Count: counts[lib.ID]})
	}
	return out
}

func makeItemsFromMap(counts map[string]int) []CategoryItem {
	out := make([]CategoryItem, 0, len(counts))
	for key, count := range counts {
		out = append(out, CategoryItem{ID: key, Label: key, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return naturalLess(out[i].Label, out[j].Label)
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func (s *apiServer) handleComics(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	page := max(1, parseInt(r.URL.Query().Get("page"), 1))
	pageSize := max(1, parseInt(r.URL.Query().Get("page_size"), 24))
	category := r.URL.Query().Get("category")
	if category == "" {
		category = "all"
	}
	comics := s.filteredComics(r.URL.Query().Get("library_id"), category, r.URL.Query().Get("param"), r.URL.Query().Get("sort"))
	paged, maxPage := paginate(comics, page, pageSize)
	writeData(w, map[string]any{
		"items":  s.toCards(base, paged, false),
		"paging": map[string]any{"page": page, "page_size": pageSize, "max_page": maxPage, "total": len(comics)},
	})
}

func (s *apiServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	base := baseURL(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := max(1, parseInt(r.URL.Query().Get("page"), 1))
	pageSize := max(1, parseInt(r.URL.Query().Get("page_size"), 24))
	comics := s.searchComics(r.URL.Query().Get("library_id"), q, r.URL.Query().Get("sort"))
	paged, maxPage := paginate(comics, page, pageSize)
	writeData(w, map[string]any{
		"items":  s.toCards(base, paged, false),
		"paging": map[string]any{"page": page, "page_size": pageSize, "max_page": maxPage, "total": len(comics)},
	})
}

func (s *apiServer) handleComicSubtree(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/v1/comics/")
	parts := strings.Split(rel, "/")
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleComicDetails(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "thumbnails" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleComicThumbnails(w, r, parts[0])
		return
	}
	if len(parts) == 4 && parts[1] == "chapters" && parts[3] == "pages" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleChapterPages(w, r, parts[0], parts[2])
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "endpoint not found")
}

func (s *apiServer) handleComicDetails(w http.ResponseWriter, r *http.Request, comicID string) {
	base := baseURL(r)
	comic := s.app.comicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	folders := s.app.favorites.ComicFolders(comicID)
	chapters := make([]map[string]any, 0, len(comic.Chapters))
	for _, chapter := range comic.Chapters {
		pages, _ := s.app.materializeChapterPages(context.Background(), chapter)
		chapter.PageCount = len(pages)
		chapters = append(chapters, map[string]any{
			"id":         chapter.ID,
			"title":      chapter.Title,
			"index":      chapter.Index,
			"page_count": chapter.PageCount,
		})
	}
	tags := map[string][]string{
		"Author":  comic.Authors,
		"Tag":     comic.Tags,
		"Library": []string{comic.LibraryName},
		"Storage": []string{comic.Storage},
	}
	recommend := s.relatedComics(comic)
	writeData(w, map[string]any{
		"id":          comic.ID,
		"title":       comic.Title,
		"subtitle":    comic.Subtitle,
		"cover_url":   s.mediaURL(base, signedPayload{Type: "cover", ComicID: comic.ID}),
		"description": comic.Description,
		"tags":        tags,
		"chapters":    chapters,
		"favorite":    map[string]any{"is_favorited": len(folders) > 0, "folder_ids": folders},
		"recommend":   s.toCards(base, recommend, false),
		"update_time": comic.UpdatedAt.Format(time.RFC3339),
		"upload_time": comic.AddedAt.Format(time.RFC3339),
		"source_url":  comic.SourceURL,
	})
}

func (s *apiServer) handleComicThumbnails(w http.ResponseWriter, r *http.Request, comicID string) {
	comic := s.app.comicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	offset := parseInt(r.URL.Query().Get("next"), 0)
	chapter := comic.Chapters[0]
	pages, err := s.app.materializeChapterPages(context.Background(), chapter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	next := ""
	end := offset + 20
	if end < len(pages) {
		next = strconv.Itoa(end)
	} else {
		end = len(pages)
	}
	base := baseURL(r)
	thumbs := make([]string, 0, end-offset)
	for _, page := range pages[offset:end] {
		thumbs = append(thumbs, s.mediaURL(base, signedPayload{Type: "page", ComicID: comic.ID, ChapterID: chapter.ID, PageIndex: page.PageIndex}))
	}
	writeData(w, map[string]any{"thumbnails": thumbs, "next": emptyToNil(next)})
}

func (s *apiServer) handleChapterPages(w http.ResponseWriter, r *http.Request, comicID, chapterID string) {
	comic := s.app.comicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	chapter := s.app.chapters[chapterID]
	if chapter == nil || chapter.ComicID != comicID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "chapter not found")
		return
	}
	pages, err := s.app.materializeChapterPages(context.Background(), chapter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	base := baseURL(r)
	images := make([]string, 0, len(pages))
	for _, page := range pages {
		images = append(images, s.mediaURL(base, signedPayload{Type: "page", ComicID: comicID, ChapterID: chapterID, PageIndex: page.PageIndex}))
	}
	writeData(w, map[string]any{"images": images})
}

func (s *apiServer) handleFavoriteFolders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		folders := s.app.favorites.ListFolders()
		writeData(w, map[string]any{
			"folders":   folders,
			"favorited": s.app.favorites.ComicFolders(r.URL.Query().Get("comic_id")),
		})
	case http.MethodPost:
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.Name) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "name is required")
			return
		}
		folder, err := s.app.favorites.AddFolder(payload.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, folder)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *apiServer) handleFavoriteFolderDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/favorites/folders/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "folder_id is required")
		return
	}
	if err := s.app.favorites.DeleteFolder(id); err != nil {
		writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
		return
	}
	writeData(w, map[string]any{"ok": true})
}

func (s *apiServer) handleFavoriteComics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	folderID := r.URL.Query().Get("folder_id")
	page := max(1, parseInt(r.URL.Query().Get("page"), 1))
	pageSize := max(1, parseInt(r.URL.Query().Get("page_size"), 24))
	ids := s.app.favorites.FolderComicIDs(folderID)
	comics := make([]*Comic, 0, len(ids))
	for _, id := range ids {
		if comic := s.app.comicByID(id); comic != nil {
			comics = append(comics, comic)
		}
	}
	paged, maxPage := paginate(comics, page, pageSize)
	writeData(w, map[string]any{
		"items":  s.toCards(baseURL(r), paged, true),
		"paging": map[string]any{"page": page, "page_size": pageSize, "max_page": maxPage, "total": len(comics)},
	})
}

func (s *apiServer) handleFavoriteItems(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var payload struct {
			ComicID  string `json:"comic_id"`
			FolderID string `json:"folder_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid payload")
			return
		}
		if err := s.app.favorites.AddItem(payload.FolderID, payload.ComicID); err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.app.favorites.RemoveItem(r.URL.Query().Get("folder_id"), r.URL.Query().Get("comic_id")); err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *apiServer) handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload struct {
		LibraryID string `json:"library_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	jobID := shaID("rescan", payload.LibraryID, time.Now().Format(time.RFC3339Nano))
	go func() {
		if err := s.app.Rescan(context.Background(), payload.LibraryID); err != nil {
			s.log.Printf("rescan failed: %v", err)
		}
	}()
	writeData(w, map[string]any{"job_id": jobID, "status": "queued"})
}

func (s *apiServer) handleMetadataRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	writeData(w, map[string]any{"job_id": shaID("metadata", time.Now().Format(time.RFC3339Nano)), "status": "queued"})
}

func (s *apiServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	token, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/media/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_TOKEN", "invalid token encoding")
		return
	}
	payload, err := parseSignedPayload(s.app.cfg.Server.Token, token)
	if err != nil {
		writeError(w, http.StatusForbidden, "INVALID_MEDIA_TOKEN", err.Error())
		return
	}
	switch payload.Type {
	case "cover":
		s.serveCover(w, r, payload)
	case "page":
		s.servePage(w, r, payload)
	default:
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_TYPE", "unsupported media type")
	}
}

func mediaETag(r *http.Request) string {
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	if token == "" {
		return ""
	}
	return `"` + token + `"`
}

func setMediaCacheHeaders(w http.ResponseWriter, r *http.Request) bool {
	etag := mediaETag(r)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if etag != "" && strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func (s *apiServer) archivePageCachePath(ctx context.Context, backend Backend, comic *Comic, page PageRef) (string, error) {
	rc, size, modTime, err := backend.OpenStream(ctx, page.SourceRef)
	if err != nil {
		return "", err
	}
	_ = rc.Close()
	ext := strings.ToLower(filepath.Ext(page.EntryName))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(page.Name))
	}
	key := shaID(comic.LibraryID, cleanRel(page.SourceRef), cleanRel(page.EntryName), strconv.FormatInt(size, 10), modTime.UTC().Format(time.RFC3339Nano))
	return filepath.Join(s.app.cfg.Server.CacheDir, "page-media", key+ext), nil
}

func (s *apiServer) serveFilePath(w http.ResponseWriter, contentType string, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	_, err = io.Copy(w, f)
	return err
}

func (s *apiServer) serveArchivePageFromCache(w http.ResponseWriter, r *http.Request, backend Backend, comic *Comic, page PageRef) bool {
	cachePath, err := s.archivePageCachePath(r.Context(), backend, comic, page)
	if err != nil {
		return false
	}
	if _, err := os.Stat(cachePath); err != nil {
		return false
	}
	if err := s.serveFilePath(w, guessContentType(page.Name), cachePath); err != nil {
		s.log.Printf("page cache read failed: %v", err)
		return false
	}
	s.log.Printf("page cache hit chapter=%s entry=%s", page.SourceRef, cleanRel(page.EntryName))
	return true
}

func (s *apiServer) ensureArchivePageCached(ctx context.Context, backend Backend, comic *Comic, page PageRef) (string, bool, error) {
	cachePath, err := s.archivePageCachePath(ctx, backend, comic, page)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, false, nil
	}
	archive, err := openArchive(ctx, backend, page.SourceRef, s.app.cfg.Server.CacheDir)
	if err != nil {
		return "", false, err
	}
	defer archive.Close()
	rc, err := archive.Open(ctx, page.EntryName)
	if err != nil {
		return "", false, err
	}
	defer rc.Close()
	tmpPath := cachePath + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := copyFile(tmpPath, rc); err != nil {
		_ = os.Remove(tmpPath)
		return "", false, err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		if _, statErr := os.Stat(cachePath); statErr != nil {
			_ = os.Remove(tmpPath)
			return "", false, err
		}
		_ = os.Remove(tmpPath)
		return cachePath, false, nil
	}
	return cachePath, true, nil
}

func (s *apiServer) materializeArchivePageCache(w http.ResponseWriter, r *http.Request, backend Backend, comic *Comic, page PageRef) bool {
	cachePath, created, err := s.ensureArchivePageCached(r.Context(), backend, comic, page)
	if err != nil {
		return false
	}
	if created {
		s.log.Printf("page cache fill chapter=%s entry=%s", page.SourceRef, cleanRel(page.EntryName))
	}
	if err := s.serveFilePath(w, guessContentType(page.Name), cachePath); err != nil {
		s.log.Printf("page cache serve failed: %v", err)
		return false
	}
	return true
}

func (s *apiServer) prefetchOrder(total, current int) []int {
	if total <= 1 {
		return nil
	}
	out := make([]int, 0, total-1)
	for i := current + 1; i < total && i <= current+3; i++ {
		out = append(out, i)
	}
	for i := current + 4; i < total; i++ {
		out = append(out, i)
	}
	for i := current - 1; i >= 0; i-- {
		out = append(out, i)
	}
	return out
}

func (s *apiServer) scheduleChapterPrefetch(chapter *Chapter, pages []PageRef, currentIndex int) {
	if chapter == nil || chapter.SourceType != "archive" || currentIndex < 0 || currentIndex >= len(pages) {
		return
	}
	key := chapter.ID
	s.prefetchMu.Lock()
	if _, exists := s.prefetching[key]; exists {
		s.prefetchMu.Unlock()
		s.log.Printf("page prefetch skip chapter=%s reason=already-running", chapter.ID)
		return
	}
	s.prefetching[key] = struct{}{}
	s.prefetchMu.Unlock()
	go func() {
		s.prefetchSem <- struct{}{}
		defer func() {
			<-s.prefetchSem
			s.prefetchMu.Lock()
			delete(s.prefetching, key)
			s.prefetchMu.Unlock()
		}()
		comic := s.app.comicByID(chapter.ComicID)
		if comic == nil {
			return
		}
		backend := s.app.backends[comic.LibraryID]
		if backend == nil {
			return
		}
		order := s.prefetchOrder(len(pages), currentIndex)
		s.log.Printf("page prefetch start chapter=%s from=%d queued=%d", chapter.ID, currentIndex, len(order))
		ctx := context.Background()
		createdCount := 0
		hitCount := 0
		failedCount := 0
		for _, index := range order {
			page := pages[index]
			if page.SourceType != "archive" {
				continue
			}
			_, created, err := s.ensureArchivePageCached(ctx, backend, comic, page)
			if err != nil {
				failedCount++
				s.log.Printf("page prefetch failed: chapter=%s page=%d err=%v", chapter.ID, index, err)
				continue
			}
			if created {
				createdCount++
			} else {
				hitCount++
			}
		}
		s.log.Printf("page prefetch done chapter=%s warmed=%d hit=%d failed=%d", chapter.ID, createdCount, hitCount, failedCount)
	}()
}

func (s *apiServer) serveCover(w http.ResponseWriter, r *http.Request, payload *signedPayload) {
	comic := s.app.comicByID(payload.ComicID)
	if comic == nil || len(comic.Chapters) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	pages, err := s.app.materializeChapterPages(r.Context(), comic.Chapters[0])
	if err != nil || len(pages) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		return
	}
	s.serveResolvedPage(w, r, comic.Chapters[0], pages[0])
}

func (s *apiServer) servePage(w http.ResponseWriter, r *http.Request, payload *signedPayload) {
	chapter := s.app.chapters[payload.ChapterID]
	if chapter == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "chapter not found")
		return
	}
	pages, err := s.app.materializeChapterPages(r.Context(), chapter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	if payload.PageIndex < 0 || payload.PageIndex >= len(pages) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "page not found")
		return
	}
	s.scheduleChapterPrefetch(chapter, pages, payload.PageIndex)
	s.serveResolvedPage(w, r, chapter, pages[payload.PageIndex])
}

func (s *apiServer) serveResolvedPage(w http.ResponseWriter, r *http.Request, chapter *Chapter, page PageRef) {
	if setMediaCacheHeaders(w, r) {
		return
	}
	comic := s.app.comicByID(chapter.ComicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	backend := s.app.backends[comic.LibraryID]
	switch page.SourceType {
	case "file":
		rc, _, _, err := backend.OpenStream(r.Context(), page.SourceRef)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", guessContentType(page.Name))
		_, _ = io.Copy(w, rc)
	case "archive":
		if s.serveArchivePageFromCache(w, r, backend, comic, page) {
			return
		}
		if s.materializeArchivePageCache(w, r, backend, comic, page) {
			return
		}
		archive, err := openArchive(r.Context(), backend, page.SourceRef, s.app.cfg.Server.CacheDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
			return
		}
		defer archive.Close()
		rc, err := archive.Open(r.Context(), page.EntryName)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "page not found")
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", guessContentType(page.Name))
		_, _ = io.Copy(w, rc)
		return
	default:
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_TYPE", "unsupported media source type")
	}
}

func (s *apiServer) filteredComics(libraryID, category, param, sortKey string) []*Comic {
	s.app.comicsMu.RLock()
	defer s.app.comicsMu.RUnlock()
	out := make([]*Comic, 0, len(s.app.comics))
	for _, comic := range s.app.comics {
		if libraryID != "" && comic.LibraryID != libraryID {
			continue
		}
		switch category {
		case "library":
			if param != "" && comic.LibraryID != param {
				continue
			}
		case "tag":
			if param != "" && !containsString(comic.Tags, param) {
				continue
			}
		case "author":
			if param != "" && !containsString(comic.Authors, param) {
				continue
			}
		case "storage":
			if param != "" && comic.Storage != param {
				continue
			}
		}
		out = append(out, comic)
	}
	sortComics(out, sortKey)
	return out
}

func (s *apiServer) searchComics(libraryID, q, sortKey string) []*Comic {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return s.filteredComics(libraryID, "all", "", sortKey)
	}
	mode := ""
	needle := q
	if strings.Contains(q, ":") {
		parts := strings.SplitN(q, ":", 2)
		mode, needle = parts[0], parts[1]
	}
	s.app.comicsMu.RLock()
	defer s.app.comicsMu.RUnlock()
	out := []*Comic{}
	for _, comic := range s.app.comics {
		if libraryID != "" && comic.LibraryID != libraryID {
			continue
		}
		match := false
		switch mode {
		case "tag":
			match = containsStringFold(comic.Tags, needle)
		case "author":
			match = containsStringFold(comic.Authors, needle)
		default:
			match = strings.Contains(strings.ToLower(comic.Title), needle) ||
				strings.Contains(strings.ToLower(comic.Subtitle), needle) ||
				strings.Contains(strings.ToLower(comic.Description), needle) ||
				containsStringFold(comic.Tags, needle) ||
				containsStringFold(comic.Authors, needle)
		}
		if match {
			out = append(out, comic)
		}
	}
	sortComics(out, sortKey)
	return out
}

func (s *apiServer) relatedComics(target *Comic) []*Comic {
	out := []*Comic{}
	for _, comic := range s.filteredComics(target.LibraryID, "all", "", "updated_desc") {
		if comic.ID == target.ID {
			continue
		}
		if shareAny(comic.Tags, target.Tags) || shareAny(comic.Authors, target.Authors) {
			out = append(out, comic)
		}
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func (s *apiServer) toCards(base string, comics []*Comic, favoriteMode bool) []map[string]any {
	out := make([]map[string]any, 0, len(comics))
	for _, comic := range comics {
		card := map[string]any{
			"id":          comic.ID,
			"title":       comic.Title,
			"subtitle":    comic.Subtitle,
			"cover_url":   s.mediaURL(base, signedPayload{Type: "cover", ComicID: comic.ID}),
			"tags":        comic.Tags,
			"description": comic.Description,
		}
		if favoriteMode {
			card["favorite_id"] = comic.ID
		}
		out = append(out, card)
	}
	return out
}

func (s *apiServer) mediaURL(base string, payload signedPayload) string {
	token, _ := signPayload(s.app.cfg.Server.Token, payload)
	return strings.TrimSuffix(base, "/") + "/media/" + url.PathEscape(token)
}

func sortComics(comics []*Comic, sortKey string) {
	switch sortKey {
	case "added_desc":
		sort.Slice(comics, func(i, j int) bool { return comics[i].AddedAt.After(comics[j].AddedAt) })
	case "title_asc":
		sort.Slice(comics, func(i, j int) bool { return naturalLess(comics[i].Title, comics[j].Title) })
	case "title_desc":
		sort.Slice(comics, func(i, j int) bool { return naturalLess(comics[j].Title, comics[i].Title) })
	case "random":
		sort.Slice(comics, func(i, j int) bool { return shaID(comics[i].ID) < shaID(comics[j].ID) })
	default:
		sort.Slice(comics, func(i, j int) bool { return comics[i].UpdatedAt.After(comics[j].UpdatedAt) })
	}
}

func paginate[T any](items []T, page, pageSize int) ([]T, int) {
	total := len(items)
	maxPage := total / pageSize
	if total%pageSize != 0 {
		maxPage++
	}
	if maxPage == 0 {
		maxPage = 1
	}
	start := (page - 1) * pageSize
	if start >= total {
		return []T{}, maxPage
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], maxPage
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	if i, err := strconv.Atoi(value); err == nil {
		return i
	}
	return fallback
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

func take(items []*Comic, n int) []*Comic {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func containsStringFold(items []string, target string) bool {
	target = strings.ToLower(target)
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), target) {
			return true
		}
	}
	return false
}

func shareAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

func emptyToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
