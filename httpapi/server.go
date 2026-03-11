package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

	apppkg "venera_home_server/app"
	archivepkg "venera_home_server/archive"
	backendpkg "venera_home_server/backend"
	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

const pagePrefetchWindow = 12
const pageCacheInfoTTL = 30 * time.Second
const prefetchMinInterval = 5 * time.Second

type PageRenderMode string

const (
	pageRenderModeDefault PageRenderMode = "default"
	pageRenderModeOrigin  PageRenderMode = "origin"
)

type pageCacheInfoEntry struct {
	info    ResolvedPageCacheInfo
	expires time.Time
}

type Server struct {
	app                 *apppkg.App
	log                 *shared.LevelLogger
	PageMemoryCache     *PageMemoryCache
	pageFlightMu        sync.Mutex
	pageFlights         map[string]*pageCacheFlight
	prefetchMu          sync.Mutex
	prefetching         map[string]struct{}
	prefetchSem         chan struct{}
	prefetchLastTrigger map[string]time.Time
	pageCacheBuildSem   chan struct{}
	pageCacheInfoMu     sync.RWMutex
	pageCacheInfoItems  map[string]pageCacheInfoEntry
}

func NewHTTPServer(app *apppkg.App, logger *log.Logger) http.Handler {
	return newHTTPServer(app, logger)
}

func newHTTPServer(app *apppkg.App, logger *log.Logger) http.Handler {
	levelLogger := shared.NewLevelLogger(logger, app.Config().Server.LogLevel)
	var memoryCache *PageMemoryCache
	if app.Config().Server.MemoryCacheMB > 0 {
		memoryCache = NewPageMemoryCache(int64(app.Config().Server.MemoryCacheMB) << 20)
	}
	srv := &Server{
		app: app, log: levelLogger, PageMemoryCache: memoryCache,
		pageFlights: map[string]*pageCacheFlight{}, prefetching: map[string]struct{}{},
		prefetchSem: make(chan struct{}, 1), prefetchLastTrigger: map[string]time.Time{}, pageCacheBuildSem: make(chan struct{}, 1),
		pageCacheInfoItems: map[string]pageCacheInfoEntry{},
	}
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
	mux.HandleFunc("/api/v1/admin/metadata/enrich", srv.auth(srv.handleMetadataEnrich))
	mux.HandleFunc("/api/v1/admin/metadata/jobs", srv.auth(srv.handleMetadataJobs))
	mux.HandleFunc("/api/v1/admin/metadata/jobs/", srv.auth(srv.handleMetadataJob))
	mux.HandleFunc("/api/v1/admin/metadata/records", srv.auth(srv.handleMetadataRecords))
	mux.HandleFunc("/api/v1/admin/metadata/records/actions", srv.auth(srv.handleMetadataRecordAction))
	mux.HandleFunc("/api/v1/admin/metadata/sources", srv.auth(srv.handleMetadataSources))
	mux.HandleFunc("/api/v1/admin/metadata/sources/", srv.auth(srv.handleMetadataSources))
	mux.HandleFunc("/api/v1/admin/metadata/cleanup", srv.auth(srv.handleMetadataCleanup))
	mux.HandleFunc("/api/v1/admin/metadata/sidecar", srv.auth(srv.handleMetadataSidecar))
	mux.HandleFunc("/api/v1/admin/jobs", srv.auth(srv.handleAdminJobs))
	mux.HandleFunc("/api/v1/admin/ehbot/status", srv.auth(srv.handleEHBotStatus))
	mux.HandleFunc("/api/v1/admin/ehbot/config", srv.auth(srv.handleEHBotConfig))
	mux.HandleFunc("/api/v1/admin/ehbot/jobs", srv.auth(srv.handleEHBotJobs))
	mux.HandleFunc("/api/v1/admin/ehbot/jobs/create", srv.auth(srv.handleEHBotCreateJob))
	mux.HandleFunc("/api/v1/admin/ehbot/pull/run-once", srv.auth(srv.handleEHBotRunOnce))
	mux.HandleFunc("/media/", srv.handleMedia)
	mux.HandleFunc("/", srv.handleIndex)
	return loggingMiddleware(levelLogger, mux)
}

func NewForTests(maxMemoryBytes int64, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		log:                 shared.NewLevelLogger(logger, "debug"),
		PageMemoryCache:     NewPageMemoryCache(maxMemoryBytes),
		pageFlights:         map[string]*pageCacheFlight{},
		prefetching:         map[string]struct{}{},
		prefetchSem:         make(chan struct{}, 1),
		prefetchLastTrigger: map[string]time.Time{},
		pageCacheBuildSem:   make(chan struct{}, 1),
		pageCacheInfoItems:  map[string]pageCacheInfoEntry{},
	}
}
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(s.app.Config().Server.Token)
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

func loggingMiddleware(logger *shared.LevelLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Debugf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.app != nil {
		dataDir := strings.TrimSpace(s.app.Config().Server.DataDir)
		if dataDir != "" {
			overridePath := filepath.Join(dataDir, "admin_index.html")
			if data, err := os.ReadFile(overridePath); err == nil && len(data) > 0 {
				_, _ = w.Write(data)
				return
			}
		}
	}
	_, _ = io.WriteString(w, adminIndexHTML)
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	a := s.app
	libCounts := map[string]int{}
	for _, comic := range a.Comics() {
		libCounts[comic.LibraryID]++
	}

	libraries := make([]map[string]any, 0, len(a.Libraries()))
	for _, lib := range a.Libraries() {
		libraries = append(libraries, map[string]any{
			"id":           lib.ID,
			"name":         lib.Name,
			"kind":         lib.Kind,
			"series_count": libCounts[lib.ID],
		})
	}

	writeData(w, map[string]any{
		"server": map[string]any{
			"name":    "Venera Home Server",
			"version": "dev",
		},
		"capabilities": map[string]any{
			"favorites":      true,
			"thumbnails":     true,
			"rescan":         true,
			"metadata_fetch": a.Config().Metadata.AllowRemoteFetch,
		},
		"libraries": libraries,
		"defaults": map[string]any{
			"sort":      "updated_desc",
			"page_size": 24,
		},
	})
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	a := s.app
	libCounts := map[string]int{}
	tagCounts := map[string]int{}
	authorCounts := map[string]int{}
	storageCounts := map[string]int{}
	for _, comic := range a.Comics() {
		libCounts[comic.LibraryID]++
		storageCounts[comic.Storage]++
		for _, tag := range comic.Tags {
			tagCounts[tag]++
		}
		for _, author := range comic.Authors {
			authorCounts[author]++
		}
	}
	groups := []apppkg.CategoryGroup{
		{Key: "library", Title: "Libraries", Items: makeItemsFromLibraries(a.Libraries(), libCounts)},
		{Key: "tag", Title: "Tags", Items: makeItemsFromMap(tagCounts)},
		{Key: "author", Title: "Authors", Items: makeItemsFromMap(authorCounts)},
		{Key: "storage", Title: "Storage", Items: makeItemsFromMap(storageCounts)},
	}
	writeData(w, map[string]any{"groups": groups})
}

func makeItemsFromLibraries(libs []configpkg.LibraryConfig, counts map[string]int) []apppkg.CategoryItem {
	out := make([]apppkg.CategoryItem, 0, len(libs))
	for _, lib := range libs {
		out = append(out, apppkg.CategoryItem{ID: lib.ID, Label: lib.Name, Count: counts[lib.ID]})
	}
	return out
}

func makeItemsFromMap(counts map[string]int) []apppkg.CategoryItem {
	out := make([]apppkg.CategoryItem, 0, len(counts))
	for key, count := range counts {
		out = append(out, apppkg.CategoryItem{ID: key, Label: key, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return shared.NaturalLess(out[i].Label, out[j].Label)
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func (s *Server) handleComics(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleComicSubtree(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleComicDetails(w http.ResponseWriter, r *http.Request, comicID string) {
	base := baseURL(r)
	comic := s.app.ComicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	folders := s.app.Favorites().ComicFolders(comicID)
	chapters := make([]map[string]any, 0, len(comic.Chapters))
	for _, chapter := range comic.Chapters {
		pages, _ := s.app.MaterializeChapterPages(context.Background(), chapter)
		chapter.PageCount = len(pages)
		chapters = append(chapters, map[string]any{
			"id":         chapter.ID,
			"title":      chapter.Title,
			"index":      chapter.Index,
			"page_count": chapter.PageCount,
		})
	}
	relativePath := s.comicRelativePath(comic)
	localPath := s.comicLocalPath(comic)
	tags := buildComicTagGroups(comic, true)
	recommend := s.relatedComics(comic)
	writeData(w, map[string]any{
		"id":            comic.ID,
		"title":         comic.Title,
		"subtitle":      comic.Subtitle,
		"cover_url":     s.mediaURL(base, shared.SignedPayload{Type: "cover", ComicID: comic.ID}),
		"description":   comic.Description,
		"tags":          tags,
		"chapters":      chapters,
		"favorite":      map[string]any{"is_favorited": len(folders) > 0, "folder_ids": folders},
		"recommend":     s.toCards(base, recommend, false),
		"update_time":   comic.UpdatedAt.Format(time.RFC3339),
		"upload_time":   comic.AddedAt.Format(time.RFC3339),
		"source_url":    comic.SourceURL,
		"relative_path": relativePath,
		"local_path":    localPath,
	})
}

func (s *Server) comicRelativePath(comic *apppkg.Comic) string {
	if comic == nil {
		return ""
	}
	return shared.CleanRel(comic.RootRef)
}

func (s *Server) comicLocalPath(comic *apppkg.Comic) string {
	if comic == nil {
		return ""
	}
	relativePath := s.comicRelativePath(comic)
	for _, lib := range s.app.Config().Libraries {
		if lib.ID == comic.LibraryID {
			return resolveLibraryRootPath(lib, relativePath)
		}
	}
	return relativePath
}

func resolveLibraryRootPath(lib configpkg.LibraryConfig, rootRef string) string {
	cleaned := shared.CleanRel(rootRef)
	switch strings.ToLower(strings.TrimSpace(lib.Kind)) {
	case "local":
		if cleaned == "" {
			return filepath.Clean(lib.Root)
		}
		return filepath.Clean(filepath.Join(lib.Root, filepath.FromSlash(cleaned)))
	case "smb":
		base := `\\` + strings.TrimSpace(lib.Host) + `\` + strings.Trim(strings.ReplaceAll(lib.Share, "/", `\`), `\`)
		if cleaned == "" {
			return base
		}
		return base + `\` + strings.ReplaceAll(cleaned, "/", `\`)
	case "webdav":
		base := strings.TrimRight(strings.TrimSpace(lib.URL), "/")
		if cleaned == "" {
			return base
		}
		return base + "/" + cleaned
	default:
		return cleaned
	}
}

func (s *Server) handleComicThumbnails(w http.ResponseWriter, r *http.Request, comicID string) {
	comic := s.app.ComicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	offset := parseInt(r.URL.Query().Get("next"), 0)
	chapter := comic.Chapters[0]
	pages, err := s.app.MaterializeChapterPages(context.Background(), chapter)
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
		thumbs = append(thumbs, s.mediaURL(base, shared.SignedPayload{Type: "page", ComicID: comic.ID, ChapterID: chapter.ID, PageIndex: page.PageIndex}))
	}
	writeData(w, map[string]any{"thumbnails": thumbs, "next": emptyToNil(next)})
}

func (s *Server) handleChapterPages(w http.ResponseWriter, r *http.Request, comicID, chapterID string) {
	comic := s.app.ComicByID(comicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	chapter := s.app.ChapterByID(chapterID)
	if chapter == nil || chapter.ComicID != comicID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "chapter not found")
		return
	}
	pages, err := s.app.MaterializeChapterPages(context.Background(), chapter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	base := baseURL(r)
	images := make([]string, 0, len(pages))
	for _, page := range pages {
		images = append(images, s.mediaURL(base, shared.SignedPayload{Type: "page", ComicID: comicID, ChapterID: chapterID, PageIndex: page.PageIndex}))
	}
	writeData(w, map[string]any{"images": images})
}

func (s *Server) handleFavoriteFolders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		folders := s.app.Favorites().ListFolders()
		writeData(w, map[string]any{
			"folders":   folders,
			"favorited": s.app.Favorites().ComicFolders(r.URL.Query().Get("comic_id")),
		})
	case http.MethodPost:
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.Name) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_INPUT", "name is required")
			return
		}
		folder, err := s.app.Favorites().AddFolder(payload.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, folder)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleFavoriteFolderDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/favorites/folders/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "folder_id is required")
		return
	}
	if err := s.app.Favorites().DeleteFolder(id); err != nil {
		writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
		return
	}
	writeData(w, map[string]any{"ok": true})
}

func (s *Server) handleFavoriteComics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	folderID := r.URL.Query().Get("folder_id")
	page := max(1, parseInt(r.URL.Query().Get("page"), 1))
	pageSize := max(1, parseInt(r.URL.Query().Get("page_size"), 24))
	ids := s.app.Favorites().FolderComicIDs(folderID)
	comics := make([]*apppkg.Comic, 0, len(ids))
	for _, id := range ids {
		if comic := s.app.ComicByID(id); comic != nil {
			comics = append(comics, comic)
		}
	}
	paged, maxPage := paginate(comics, page, pageSize)
	writeData(w, map[string]any{
		"items":  s.toCards(baseURL(r), paged, true),
		"paging": map[string]any{"page": page, "page_size": pageSize, "max_page": maxPage, "total": len(comics)},
	})
}

func (s *Server) handleFavoriteItems(w http.ResponseWriter, r *http.Request) {
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
		if err := s.app.Favorites().AddItem(payload.FolderID, payload.ComicID); err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.app.Favorites().RemoveItem(r.URL.Query().Get("folder_id"), r.URL.Query().Get("comic_id")); err != nil {
			writeError(w, http.StatusInternalServerError, "WRITE_FAILED", err.Error())
			return
		}
		writeData(w, map[string]any{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload struct {
		LibraryID string `json:"library_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}
	job, err := s.app.StartMetadataRefresh(r.Context(), apppkg.MetadataRefreshRequest{LibraryID: strings.TrimSpace(payload.LibraryID), Trigger: "rescan"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RESCAN_FAILED", err.Error())
		return
	}
	writeData(w, job)
}

func (s *Server) handleMetadataRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload apppkg.MetadataRefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}
	payload.Trigger = normalizeRequestValue(payload.Trigger, "refresh")
	job, err := s.app.StartMetadataRefresh(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_REFRESH_FAILED", err.Error())
		return
	}
	writeData(w, job)
}

func (s *Server) handleMetadataJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	jobID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/metadata/jobs/"))
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_JOB_ID", "job id is required")
		return
	}
	job, ok := s.app.MetadataJob(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "metadata job not found")
		return
	}
	writeData(w, job)
}

func (s *Server) handleMetadataRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	page := parsePage(strings.TrimSpace(r.URL.Query().Get("page")))
	limit := parseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 200)
	if limit <= 0 {
		limit = 200
	}
	query := metadatapkg.ListQuery{
		State:     strings.TrimSpace(r.URL.Query().Get("state")),
		LibraryID: strings.TrimSpace(r.URL.Query().Get("library_id")),
		Path:      strings.TrimSpace(r.URL.Query().Get("path")),
		Search:    strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:     limit,
		Offset:    (page - 1) * limit,
	}
	result, err := s.app.MetadataRecordsPage(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_RECORDS_FAILED", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, record := range result.Items {
		items = append(items, metadataRecordMap(record))
	}
	writeData(w, map[string]any{
		"items":     items,
		"count":     len(items),
		"total":     result.Total,
		"page":      page,
		"page_size": result.Limit,
		"offset":    result.Offset,
	})
}

func (s *Server) handleMetadataCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload metadatapkg.CleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}
	result, err := s.app.CleanupMetadataTracked(r.Context(), payload, "manual")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_CLEANUP_FAILED", err.Error())
		return
	}
	writeData(w, result)
}

func metadataRecordState(record metadatapkg.Record) string {
	if record.ManualLocked {
		return "locked"
	}
	if record.MissingSince != nil {
		return "missing"
	}
	if strings.TrimSpace(record.LastError) != "" {
		return "error"
	}
	if record.StaleAfter != nil && !record.StaleAfter.After(time.Now().UTC()) {
		return "stale"
	}
	if record.IsEmptyMetadata() {
		return "empty"
	}
	return "ready"
}

func formatTimePtrRFC3339(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeRequestValue(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	token, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/media/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_TOKEN", "invalid token encoding")
		return
	}
	payload, err := shared.ParseSignedPayload(s.app.Config().Server.Token, token)
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
	return `"` + token + "|" + string(pageRenderModeFromRequest(r)) + `"`
}

func setMediaCacheHeaders(w http.ResponseWriter, r *http.Request) bool {
	etag := mediaETag(r)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Vary", "X-Venera-Image-Mode")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if etag != "" && strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func normalizePageRenderMode(raw string) PageRenderMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "origin", "original":
		return pageRenderModeOrigin
	default:
		return pageRenderModeDefault
	}
}

func pageRenderModeFromRequest(r *http.Request) PageRenderMode {
	if r == nil {
		return pageRenderModeOrigin
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("mode")); raw != "" {
		return normalizePageRenderMode(raw)
	}
	if raw := strings.TrimSpace(r.Header.Get("X-Venera-Image-Mode")); raw != "" {
		return normalizePageRenderMode(raw)
	}
	if strings.TrimSpace(r.Header.Get("X-Venera-Reader")) == "1" {
		return pageRenderModeDefault
	}
	return pageRenderModeOrigin
}

type ResolvedPageCacheInfo struct {
	Key              string
	Path             string
	ContentType      string
	ModTime          time.Time
	ContentLength    int64
	Mode             PageRenderMode
	VisualCompressed bool
}

func pageCacheLabel(page apppkg.PageRef) string {
	if page.SourceType == "archive" {
		return shared.CleanRel(page.SourceRef) + "::" + shared.CleanRel(page.EntryName)
	}
	return shared.CleanRel(page.SourceRef)
}

func (s *Server) pageSourceMeta(ctx context.Context, backend backendpkg.Backend, page apppkg.PageRef) (int64, time.Time, error) {
	if page.Size > 0 || !page.ModTime.IsZero() {
		return page.Size, page.ModTime, nil
	}
	if page.SourceType == "archive" && page.EntryName != "" {
		archive, err := archivepkg.Open(ctx, backend, page.SourceRef, s.app.Config().Server.CacheDir)
		if err != nil {
			return 0, time.Time{}, err
		}
		defer archive.Close()
		target := shared.CleanRel(strings.ReplaceAll(page.EntryName, "\\", "/"))
		for _, entry := range archive.Entries() {
			if shared.CleanRel(strings.ReplaceAll(entry.Name, "\\", "/")) == target {
				return entry.Size, entry.ModTime, nil
			}
		}
		return 0, time.Time{}, os.ErrNotExist
	}
	rc, size, modTime, err := backend.OpenStream(ctx, page.SourceRef)
	if err != nil {
		return 0, time.Time{}, err
	}
	_ = rc.Close()
	return size, modTime, nil
}

func (s *Server) pageCacheInfo(ctx context.Context, backend backendpkg.Backend, comic *apppkg.Comic, page apppkg.PageRef, mode PageRenderMode) (ResolvedPageCacheInfo, error) {
	size, modTime, err := s.pageSourceMeta(ctx, backend, page)
	if err != nil {
		return ResolvedPageCacheInfo{}, err
	}
	sourceExt := pageSourceExt(page)
	contentType := shared.GuessContentType(page.Name)
	cacheExt := sourceExt
	cacheContentType := contentType
	visualCompressed := false
	if mode == pageRenderModeDefault {
		compressed, width, height, compressErr := s.shouldVisualCompressPage(ctx, backend, page, contentType, sourceExt, size)
		if compressErr != nil {
			s.log.Debugf("page visual compress probe failed ref=%s err=%v", pageCacheLabel(page), compressErr)
		} else if compressed {
			visualCompressed = true
			s.log.Debugf("page visual compress plan ref=%s mode=%s size=%d dims=%dx%d", pageCacheLabel(page), mode, size, width, height)
		}
	}
	if visualCompressed {
		cacheExt = ".jpg"
		cacheContentType = "image/jpeg"
	}
	parts := []string{"page", renderedPageCacheVariant, string(mode), page.SourceType, comic.LibraryID, shared.CleanRel(page.SourceRef), strconv.FormatInt(size, 10), modTime.UTC().Format(time.RFC3339Nano)}
	if page.EntryName != "" {
		parts = append(parts, shared.CleanRel(page.EntryName))
	}
	parts = append(parts, cacheContentType)
	contentLength := int64(0)
	if page.SourceType == "file" && cacheContentType == contentType {
		contentLength = size
	}
	return ResolvedPageCacheInfo{
		Key:              shared.SHAID(parts...),
		Path:             filepath.Join(s.app.Config().Server.CacheDir, "rendered-pages", shared.SHAID(parts...)+cacheExt),
		ContentType:      cacheContentType,
		ModTime:          modTime,
		ContentLength:    contentLength,
		Mode:             mode,
		VisualCompressed: visualCompressed,
	}, nil
}

func pageCacheInfoLookupKey(comic *apppkg.Comic, page apppkg.PageRef, mode PageRenderMode) string {
	if page.EntryName != "" {
		return comic.LibraryID + ":" + string(mode) + ":" + page.SourceRef + "::" + page.EntryName
	}
	return comic.LibraryID + ":" + string(mode) + ":" + page.SourceRef
}

func (s *Server) pageCacheInfoCached(ctx context.Context, backend backendpkg.Backend, comic *apppkg.Comic, page apppkg.PageRef, mode PageRenderMode) (ResolvedPageCacheInfo, error) {
	key := pageCacheInfoLookupKey(comic, page, mode)
	now := time.Now()

	s.pageCacheInfoMu.RLock()
	if entry, ok := s.pageCacheInfoItems[key]; ok && now.Before(entry.expires) {
		s.pageCacheInfoMu.RUnlock()
		return entry.info, nil
	}
	s.pageCacheInfoMu.RUnlock()

	info, err := s.pageCacheInfo(ctx, backend, comic, page, mode)
	if err != nil {
		return info, err
	}

	s.pageCacheInfoMu.Lock()
	s.pageCacheInfoItems[key] = pageCacheInfoEntry{info: info, expires: now.Add(pageCacheInfoTTL)}
	// Trim stale entries periodically
	if len(s.pageCacheInfoItems) > 500 {
		for k, v := range s.pageCacheInfoItems {
			if now.After(v.expires) {
				delete(s.pageCacheInfoItems, k)
			}
		}
	}
	s.pageCacheInfoMu.Unlock()

	return info, nil
}

func (s *Server) serveFilePath(w http.ResponseWriter, contentType string, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil {
		if info.Size() > 0 && w.Header().Get("Content-Length") == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		}
		if !info.ModTime().IsZero() && w.Header().Get("Last-Modified") == "" {
			w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentType)
	}
	_, err = io.Copy(w, f)
	return err
}

func (s *Server) servePageBytes(w http.ResponseWriter, contentType string, modTime time.Time, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if !modTime.IsZero() {
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
	}
	_, _ = w.Write(data)
}

func (s *Server) servePageFromMemoryCache(w http.ResponseWriter, info ResolvedPageCacheInfo, page apppkg.PageRef) bool {
	entry, ok := s.PageMemoryCache.Get(info.Key)
	if !ok {
		return false
	}
	contentType := entry.ContentType
	if contentType == "" {
		contentType = info.ContentType
	}
	s.servePageBytes(w, contentType, entry.ModTime, entry.Data)
	s.log.Debugf("page memory hit ref=%s bytes=%d", pageCacheLabel(page), len(entry.Data))
	return true
}

func (s *Server) ServePageFromDiskCache(w http.ResponseWriter, info ResolvedPageCacheInfo, page apppkg.PageRef) bool {
	if _, err := os.Stat(info.Path); err != nil {
		return false
	}
	if !info.ModTime.IsZero() {
		w.Header().Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Content-Type", info.ContentType)
	if err := s.serveFilePath(w, info.ContentType, info.Path); err != nil {
		s.log.Debugf("render cache serve failed: %v", err)
		return false
	}
	s.schedulePageMemoryWarm(info, page)
	s.log.Debugf("render cache hit ref=%s", pageCacheLabel(page))
	return true
}

func (s *Server) writePageCacheAtomically(path string, write func(string) error) (bool, error) {
	if err := shared.EnsureDir(filepath.Dir(path)); err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	tmpPath := path + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := write(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			_ = os.Remove(tmpPath)
			return false, nil
		}
		_ = os.Remove(tmpPath)
		return false, err
	}
	return true, nil
}

func (s *Server) writePageCacheBytes(info ResolvedPageCacheInfo, data []byte) (bool, error) {
	return s.writePageCacheAtomically(info.Path, func(tmpPath string) error {
		return os.WriteFile(tmpPath, data, 0o644)
	})
}

func (s *Server) schedulePageCachePersist(info ResolvedPageCacheInfo, page apppkg.PageRef, data []byte) {
	if len(data) == 0 {
		return
	}
	dataCopy := append([]byte(nil), data...)
	go func() {
		time.Sleep(200 * time.Millisecond)
		created, err, shared := s.DoPageFlight(info.Key, func() (bool, error) {
			return s.writePageCacheBytes(info, dataCopy)
		})
		if err != nil {
			s.log.Debugf("render cache async persist failed ref=%s err=%v", pageCacheLabel(page), err)
			return
		}
		if created && !shared {
			s.log.Debugf("render cache async persist ref=%s", pageCacheLabel(page))
		}
	}()
}

func (s *Server) ensurePageCached(ctx context.Context, backend backendpkg.Backend, page apppkg.PageRef, info ResolvedPageCacheInfo) (bool, error) {
	if _, err := os.Stat(info.Path); err == nil {
		return false, nil
	}
	var data []byte
	switch page.SourceType {
	case "file":
		rc, _, _, err := backend.OpenStream(ctx, page.SourceRef)
		if err != nil {
			return false, err
		}
		defer rc.Close()
		data, err = io.ReadAll(rc)
		if err != nil {
			return false, err
		}
	case "archive":
		archive, err := archivepkg.Open(ctx, backend, page.SourceRef, s.app.Config().Server.CacheDir)
		if err != nil {
			return false, err
		}
		defer archive.Close()
		rc, err := archive.Open(ctx, page.EntryName)
		if err != nil {
			return false, err
		}
		defer rc.Close()
		data, err = io.ReadAll(rc)
		if err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported media source type: %s", page.SourceType)
	}
	return s.storePageCacheBytes(info, page, data)
}

func (s *Server) PrefetchOrder(total, current int) []int {
	if total <= 1 || current < 0 || current >= total {
		return nil
	}
	end := current + pagePrefetchWindow
	if end >= total {
		end = total - 1
	}
	if end <= current {
		return nil
	}
	out := make([]int, 0, end-current)
	for i := current + 1; i <= end; i++ {
		out = append(out, i)
	}
	return out
}

func PrefetchThrottleKey(chapterID string, windowStart int) string {
	return chapterID + ":" + strconv.Itoa(prefetchThrottleWindowStart(windowStart))
}

func (s *Server) scheduleChapterPrefetch(chapter *apppkg.Chapter, pages []apppkg.PageRef, currentIndex int) {
	if !enableServerSideChapterPrefetch {
		return
	}
	if chapter == nil || currentIndex < 0 || currentIndex >= len(pages) {
		return
	}
	order := s.PrefetchOrder(len(pages), currentIndex)
	if len(order) == 0 {
		return
	}
	windowStart := order[0]
	groupStart := prefetchThrottleWindowStart(windowStart)
	runKey := PrefetchThrottleKey(chapter.ID, windowStart)
	s.prefetchMu.Lock()
	if _, exists := s.prefetching[runKey]; exists {
		s.prefetchMu.Unlock()
		return
	}
	if last, ok := s.prefetchLastTrigger[runKey]; ok && time.Since(last) < prefetchMinInterval {
		s.prefetchMu.Unlock()
		return
	}
	s.prefetching[runKey] = struct{}{}
	s.prefetchLastTrigger[runKey] = time.Now()
	s.prefetchMu.Unlock()
	go func() {
		s.prefetchSem <- struct{}{}
		defer func() {
			<-s.prefetchSem
			s.prefetchMu.Lock()
			delete(s.prefetching, runKey)
			s.prefetchMu.Unlock()
		}()
		comic := s.app.ComicByID(chapter.ComicID)
		if comic == nil {
			return
		}
		backend := s.app.Backend(comic.LibraryID)
		if backend == nil {
			return
		}
		s.log.Debugf("page prefetch start chapter=%s from=%d window_start=%d group_start=%d queued=%d", chapter.ID, currentIndex, windowStart, groupStart, len(order))
		ctx := context.Background()
		createdCount := 0
		memoryWarmCount := 0
		memoryHitCount := 0
		diskHitCount := 0
		failedCount := 0
		for _, index := range order {
			page := pages[index]
			info, err := s.pageCacheInfoCached(ctx, backend, comic, page, pageRenderModeDefault)
			if err != nil {
				failedCount++
				s.log.Debugf("page prefetch failed: chapter=%s page=%d err=%v", chapter.ID, index, err)
				continue
			}
			if _, ok := s.PageMemoryCache.Get(info.Key); ok {
				memoryHitCount++
				continue
			}
			if warmed, err := s.WarmPageMemoryFromDiskCache(info, page); err != nil {
				failedCount++
				s.log.Debugf("page prefetch failed: chapter=%s page=%d err=%v", chapter.ID, index, err)
				continue
			} else if warmed {
				memoryWarmCount++
				continue
			}
			if _, err := os.Stat(info.Path); err == nil {
				diskHitCount++
				continue
			}
			created, err, shared := s.DoPageFlight(info.Key, func() (bool, error) {
				return s.ensurePageCached(ctx, backend, page, info)
			})
			if err != nil {
				if _, ok := s.PageMemoryCache.Get(info.Key); ok {
					memoryHitCount++
					continue
				}
				if warmed, warmErr := s.WarmPageMemoryFromDiskCache(info, page); warmErr == nil && warmed {
					memoryWarmCount++
					continue
				}
				if _, statErr := os.Stat(info.Path); statErr == nil {
					diskHitCount++
					continue
				}
				failedCount++
				s.log.Debugf("page prefetch failed: chapter=%s page=%d err=%v", chapter.ID, index, err)
				continue
			}
			if created && !shared {
				createdCount++
			} else {
				memoryHitCount++
			}
		}
		s.log.Debugf("page prefetch done chapter=%s warmed=%d memory_warm=%d memory_hit=%d disk_hit=%d failed=%d", chapter.ID, createdCount, memoryWarmCount, memoryHitCount, diskHitCount, failedCount)
	}()
}

func (s *Server) serveCover(w http.ResponseWriter, r *http.Request, payload *shared.SignedPayload) {
	comic := s.app.ComicByID(payload.ComicID)
	if comic == nil || len(comic.Chapters) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	pages, err := s.app.MaterializeChapterPages(r.Context(), comic.Chapters[0])
	if err != nil || len(pages) == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		return
	}
	s.serveResolvedPage(w, r, comic.Chapters[0], pages[0])
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request, payload *shared.SignedPayload) {
	chapter := s.app.ChapterByID(payload.ChapterID)
	if chapter == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "chapter not found")
		return
	}
	pages, err := s.app.MaterializeChapterPages(r.Context(), chapter)
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

func (s *Server) serveResolvedPage(w http.ResponseWriter, r *http.Request, chapter *apppkg.Chapter, page apppkg.PageRef) {
	if setMediaCacheHeaders(w, r) {
		return
	}
	comic := s.app.ComicByID(chapter.ComicID)
	if comic == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "comic not found")
		return
	}
	backend := s.app.Backend(comic.LibraryID)
	mode := pageRenderModeFromRequest(r)
	info, err := s.pageCacheInfoCached(r.Context(), backend, comic, page, mode)
	if err == nil {
		if s.servePageFromMemoryCache(w, info, page) {
			return
		}
		if s.ServePageFromDiskCache(w, info, page) {
			return
		}
		s.schedulePageCacheBuild(backend, info, page)
	} else {
		s.log.Debugf("render cache key failed ref=%s mode=%s err=%v", pageCacheLabel(page), mode, err)
	}
	switch page.SourceType {
	case "file":
		rc, size, modTime, err := backend.OpenStream(r.Context(), page.SourceRef)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
			return
		}
		defer rc.Close()
		contentType := shared.GuessContentType(page.Name)
		w.Header().Set("Content-Type", contentType)
		if size > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		if !modTime.IsZero() {
			w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
		}
		s.log.Debugf("page file fallback ref=%s mode=%s bytes=%d type=%s", page.SourceRef, mode, size, contentType)
		_, _ = io.Copy(w, rc)
	case "archive":
		archive, err := archivepkg.Open(r.Context(), backend, page.SourceRef, s.app.Config().Server.CacheDir)
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
		w.Header().Set("Content-Type", shared.GuessContentType(page.Name))
		_, _ = io.Copy(w, rc)
		return
	default:
		writeError(w, http.StatusBadRequest, "INVALID_MEDIA_TYPE", "unsupported media source type")
	}
}

func (s *Server) filteredComics(libraryID, category, param, sortKey string) []*apppkg.Comic {
	comics := s.app.Comics()
	out := make([]*apppkg.Comic, 0, len(comics))
	for _, comic := range comics {
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

func comicMatchesPath(comic *apppkg.Comic, needle string) bool {
	needle = strings.TrimSpace(strings.ToLower(needle))
	if needle == "" {
		return false
	}
	if strings.Contains(strings.ToLower(comic.RootRef), needle) || strings.Contains(strings.ToLower(comic.SourceURL), needle) {
		return true
	}
	for _, chapter := range comic.Chapters {
		if strings.Contains(strings.ToLower(chapter.SourceRef), needle) {
			return true
		}
	}
	return false
}

func (s *Server) searchComics(libraryID, q, sortKey string) []*apppkg.Comic {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return s.filteredComics(libraryID, "all", "", sortKey)
	}
	mode := ""
	needle := q
	if strings.Contains(q, ":") {
		parts := strings.SplitN(q, ":", 2)
		candidateMode := strings.TrimSpace(parts[0])
		candidateNeedle := strings.TrimSpace(parts[1])
		switch candidateMode {
		case "tag", "author", "path", "dir", "folder":
			mode = candidateMode
			needle = candidateNeedle
		}
	}
	out := []*apppkg.Comic{}
	for _, comic := range s.app.Comics() {
		if libraryID != "" && comic.LibraryID != libraryID {
			continue
		}
		match := false
		switch mode {
		case "tag":
			match = containsStringFold(comic.Tags, needle)
		case "author":
			match = containsStringFold(comic.Authors, needle)
		case "path", "dir", "folder":
			match = comicMatchesPath(comic, needle)
		default:
			match = strings.Contains(strings.ToLower(comic.Title), needle) ||
				strings.Contains(strings.ToLower(comic.Subtitle), needle) ||
				strings.Contains(strings.ToLower(comic.Description), needle) ||
				containsStringFold(comic.Tags, needle) ||
				containsStringFold(comic.Authors, needle) ||
				comicMatchesPath(comic, needle)
		}
		if match {
			out = append(out, comic)
		}
	}
	sortComics(out, sortKey)
	return out
}

func (s *Server) relatedComics(target *apppkg.Comic) []*apppkg.Comic {
	out := []*apppkg.Comic{}
	for _, comic := range s.filteredComics(target.LibraryID, "all", "", "updated_desc") {
		if comic.ID == target.ID {
			continue
		}
		if shared.ShareAnyFold(comic.Tags, target.Tags) || shared.ShareAnyFold(comic.Authors, target.Authors) {
			out = append(out, comic)
		}
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func (s *Server) toCards(base string, comics []*apppkg.Comic, favoriteMode bool) []map[string]any {
	out := make([]map[string]any, 0, len(comics))
	for _, comic := range comics {
		cardTags := buildComicTagGroups(comic, false)
		card := map[string]any{
			"id":          comic.ID,
			"title":       comic.Title,
			"subtitle":    comic.Subtitle,
			"cover_url":   s.mediaURL(base, shared.SignedPayload{Type: "cover", ComicID: comic.ID}),
			"tags":        cardTags,
			"description": comic.Description,
		}
		if favoriteMode {
			card["favorite_id"] = comic.ID
		}
		out = append(out, card)
	}
	return out
}

func buildComicTagGroups(comic *apppkg.Comic, includeContext bool) map[string][]string {
	tags := shared.GroupTagsByNamespace(comic.Tags, "Tag")
	if len(tags["artist"]) > 0 {
		tags["artist"] = shared.UniqueStrings(append(tags["artist"], comic.Authors...))
	} else if len(comic.Authors) > 0 {
		tags["Author"] = shared.UniqueStrings(append([]string(nil), comic.Authors...))
	}
	if len(tags["language"]) == 0 {
		if value := shared.LanguageTagValue(comic.Language); value != "" {
			tags["language"] = []string{value}
		} else if value := strings.TrimSpace(comic.Language); value != "" {
			tags["language"] = []string{value}
		}
	}
	if includeContext {
		if strings.TrimSpace(comic.LibraryName) != "" {
			tags["Library"] = []string{comic.LibraryName}
		}
		if strings.TrimSpace(comic.Storage) != "" {
			tags["Storage"] = []string{comic.Storage}
		}
	}
	return tags
}

func (s *Server) mediaURL(base string, payload shared.SignedPayload) string {
	token, _ := shared.SignPayload(s.app.Config().Server.Token, payload)
	return strings.TrimSuffix(base, "/") + "/media/" + url.PathEscape(token)
}

func sortComics(comics []*apppkg.Comic, sortKey string) {
	switch sortKey {
	case "added_desc":
		sort.Slice(comics, func(i, j int) bool { return comics[i].AddedAt.After(comics[j].AddedAt) })
	case "title_asc":
		sort.Slice(comics, func(i, j int) bool { return shared.NaturalLess(comics[i].Title, comics[j].Title) })
	case "title_desc":
		sort.Slice(comics, func(i, j int) bool { return shared.NaturalLess(comics[j].Title, comics[i].Title) })
	case "random":
		sort.Slice(comics, func(i, j int) bool { return shared.SHAID(comics[i].ID) < shared.SHAID(comics[j].ID) })
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

func take(items []*apppkg.Comic, n int) []*apppkg.Comic {
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
