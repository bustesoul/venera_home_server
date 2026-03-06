package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type App struct {
	cfg       *Config
	backends  map[string]Backend
	comicsMu  sync.RWMutex
	comics    map[string]*Comic
	libraries map[string][]string
	chapters  map[string]*Chapter
	favorites *FavoritesStore
}

type comicInfoXML struct {
	Title       string `xml:"Title"`
	Series      string `xml:"Series"`
	Summary     string `xml:"Summary"`
	Writer      string `xml:"Writer"`
	Penciller   string `xml:"Penciller"`
	Genre       string `xml:"Genre"`
	LanguageISO string `xml:"LanguageISO"`
}

func NewApp(cfg *Config) (*App, error) {
	if err := ensureDir(cfg.Server.DataDir); err != nil {
		return nil, err
	}
	if err := ensureDir(cfg.Server.CacheDir); err != nil {
		return nil, err
	}
	favorites, err := LoadFavoritesStore(cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	app := &App{
		cfg:       cfg,
		backends:  map[string]Backend{},
		comics:    map[string]*Comic{},
		libraries: map[string][]string{},
		chapters:  map[string]*Chapter{},
		favorites: favorites,
	}
	for _, lib := range cfg.Libraries {
		var backend Backend
		switch lib.Kind {
		case "local":
			backend = newLocalBackend(lib.Root)
		case "smb":
			backend, err = newSMBBackend(lib)
		case "webdav":
			backend, err = newWebDAVBackend(lib, cfg.Server.CacheDir)
		default:
			err = fmt.Errorf("unsupported library kind: %s", lib.Kind)
		}
		if err != nil {
			return nil, err
		}
		if err := backend.Connect(context.Background()); err != nil {
			return nil, err
		}
		app.backends[lib.ID] = backend
	}
	if err := app.Rescan(context.Background(), ""); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *App) libraryConfig(id string) (LibraryConfig, bool) {
	for _, lib := range a.cfg.Libraries {
		if lib.ID == id {
			return lib, true
		}
	}
	return LibraryConfig{}, false
}

func (a *App) Rescan(ctx context.Context, libraryID string) error {
	nextComics := map[string]*Comic{}
	nextLibraries := map[string][]string{}
	nextChapters := map[string]*Chapter{}

	a.comicsMu.RLock()
	if libraryID != "" {
		for id, comic := range a.comics {
			if comic.LibraryID != libraryID {
				nextComics[id] = comic
			}
		}
		for lib, ids := range a.libraries {
			if lib != libraryID {
				nextLibraries[lib] = append([]string(nil), ids...)
			}
		}
		for id, chapter := range a.chapters {
			if comic := nextComics[chapter.ComicID]; comic != nil {
				nextChapters[id] = chapter
			}
		}
	}
	a.comicsMu.RUnlock()

	for _, lib := range a.cfg.Libraries {
		if libraryID != "" && lib.ID != libraryID {
			continue
		}
		backend := a.backends[lib.ID]
		comics, err := a.scanLibrary(ctx, lib, backend)
		if err != nil {
			return fmt.Errorf("scan %s: %w", lib.ID, err)
		}
		ids := make([]string, 0, len(comics))
		for _, comic := range comics {
			ids = append(ids, comic.ID)
			nextComics[comic.ID] = comic
			for _, chapter := range comic.Chapters {
				nextChapters[chapter.ID] = chapter
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			left := nextComics[ids[i]]
			right := nextComics[ids[j]]
			return naturalLess(left.Title, right.Title)
		})
		nextLibraries[lib.ID] = ids
	}

	a.comicsMu.Lock()
	a.comics = nextComics
	a.libraries = nextLibraries
	a.chapters = nextChapters
	a.comicsMu.Unlock()
	return nil
}

func (a *App) scanLibrary(ctx context.Context, lib LibraryConfig, backend Backend) ([]*Comic, error) {
	return a.scanDir(ctx, lib, backend, "")
}

func (a *App) scanDir(ctx context.Context, lib LibraryConfig, backend Backend, rel string) ([]*Comic, error) {
	entries, err := backend.ListDir(ctx, rel)
	if err != nil {
		return nil, err
	}
	var images, archives, dirs []Entry
	for _, entry := range entries {
		if entry.IsDir {
			dirs = append(dirs, entry)
		} else if isImageFile(entry.Name) {
			images = append(images, entry)
		} else if isArchiveFile(entry.Name) {
			archives = append(archives, entry)
		}
	}
	if len(images) > 0 {
		comic, err := a.buildDirComic(ctx, lib, backend, rel, images)
		if err != nil {
			return nil, err
		}
		return []*Comic{comic}, nil
	}

	chapterDirs := make([]Entry, 0, len(dirs))
	for _, dir := range dirs {
		ok, err := a.dirLooksLikeChapter(ctx, backend, dir.RelPath)
		if err == nil && ok {
			chapterDirs = append(chapterDirs, dir)
		}
	}
	if rel != "" && len(archives)+len(chapterDirs) >= 2 {
		comic, err := a.buildSeriesComic(ctx, lib, backend, rel, archives, chapterDirs)
		if err == nil {
			return []*Comic{comic}, nil
		}
	}

	if len(archives) == 1 && len(dirs) == 0 {
		comic, err := a.buildArchiveComic(ctx, lib, backend, archives[0].RelPath, baseNameTitle(archives[0].Name))
		if err != nil {
			return nil, err
		}
		return []*Comic{comic}, nil
	}

	if len(dirs) == 1 && len(archives) == 0 && len(entries) == 1 {
		return a.scanDir(ctx, lib, backend, dirs[0].RelPath)
	}

	out := []*Comic{}
	for _, archive := range archives {
		comic, err := a.buildArchiveComic(ctx, lib, backend, archive.RelPath, baseNameTitle(archive.Name))
		if err == nil {
			out = append(out, comic)
		}
	}
	for _, dir := range dirs {
		sub, err := a.scanDir(ctx, lib, backend, dir.RelPath)
		if err == nil {
			out = append(out, sub...)
		}
	}
	return out, nil
}

func (a *App) dirLooksLikeChapter(ctx context.Context, backend Backend, rel string) (bool, error) {
	entries, err := backend.ListDir(ctx, rel)
	if err != nil {
		return false, err
	}
	imageCount := 0
	archiveCount := 0
	dirCount := 0
	for _, entry := range entries {
		if entry.IsDir {
			dirCount++
		} else if isImageFile(entry.Name) {
			imageCount++
		} else if isArchiveFile(entry.Name) {
			archiveCount++
		}
	}
	if imageCount > 0 {
		return true, nil
	}
	if archiveCount == 1 && dirCount == 0 {
		return true, nil
	}
	return false, nil
}

func (a *App) buildBaseComic(lib LibraryConfig, rel string, title string, rootType string) *Comic {
	if title == "" {
		title = baseNameTitle(rel)
	}
	now := time.Now()
	return &Comic{
		ID:          shaID(lib.ID, rootType, rel),
		LibraryID:   lib.ID,
		LibraryName: lib.Name,
		Storage:     lib.Kind,
		Title:       title,
		AddedAt:     now,
		UpdatedAt:   now,
		RootType:    rootType,
		RootRef:     cleanRel(rel),
		SourceURL:   cleanRel(rel),
		Chapters:    []*Chapter{},
	}
}

func (a *App) buildDirComic(ctx context.Context, lib LibraryConfig, backend Backend, rel string, images []Entry) (*Comic, error) {
	meta, _ := a.loadMetadataForDir(ctx, backend, rel, baseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, meta.Title, "dir")
	applyMetadata(comic, meta)
	chapter := &Chapter{
		ID:         shaID(comic.ID, "chapter", rel),
		ComicID:    comic.ID,
		Title:      comic.Title,
		Index:      1,
		SourceType: "dir",
		SourceRef:  cleanRel(rel),
		PageCount:  len(images),
	}
	comic.Chapters = []*Chapter{chapter}
	comic.UpdatedAt = latestMod(images)
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) buildArchiveComic(ctx context.Context, lib LibraryConfig, backend Backend, rel string, fallbackTitle string) (*Comic, error) {
	meta, pageCount, err := a.loadMetadataForArchive(ctx, backend, rel, fallbackTitle)
	if err != nil {
		return nil, err
	}
	comic := a.buildBaseComic(lib, rel, meta.Title, "archive")
	applyMetadata(comic, meta)
	chapter := &Chapter{
		ID:         shaID(comic.ID, "chapter", rel),
		ComicID:    comic.ID,
		Title:      comic.Title,
		Index:      1,
		SourceType: "archive",
		SourceRef:  cleanRel(rel),
		PageCount:  pageCount,
	}
	comic.Chapters = []*Chapter{chapter}
	comic.UpdatedAt = time.Now()
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) buildSeriesComic(ctx context.Context, lib LibraryConfig, backend Backend, rel string, archives []Entry, chapterDirs []Entry) (*Comic, error) {
	meta, _ := a.loadMetadataForDir(ctx, backend, rel, baseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, meta.Title, "series")
	applyMetadata(comic, meta)

	chapters := []*Chapter{}
	for _, dir := range chapterDirs {
		title := baseNameTitle(dir.Name)
		md, _ := a.loadMetadataForDir(ctx, backend, dir.RelPath, title)
		title = firstNonEmpty(md.Title, title)
		entries, _ := backend.ListDir(ctx, dir.RelPath)
		count := 0
		archiveRef := ""
		archiveName := ""
		dirCount := 0
		for _, item := range entries {
			if item.IsDir {
				dirCount++
				continue
			}
			if isImageFile(item.Name) {
				count++
			}
			if isArchiveFile(item.Name) {
				archiveRef = item.RelPath
				archiveName = item.Name
			}
		}
		sourceType := "dir"
		sourceRef := cleanRel(dir.RelPath)
		if count == 0 && archiveRef != "" && dirCount == 0 {
			sourceType = "archive"
			sourceRef = cleanRel(archiveRef)
			if md2, count2, err := a.loadMetadataForArchive(ctx, backend, archiveRef, baseNameTitle(archiveName)); err == nil {
				title = firstNonEmpty(md2.Title, title)
				count = count2
			}
		}
		chapters = append(chapters, &Chapter{
			ID:         shaID(comic.ID, "chapter", sourceRef),
			ComicID:    comic.ID,
			Title:      title,
			SourceType: sourceType,
			SourceRef:  sourceRef,
			PageCount:  count,
		})
	}
	for _, archive := range archives {
		title := baseNameTitle(archive.Name)
		md, count, _ := a.loadMetadataForArchive(ctx, backend, archive.RelPath, title)
		title = firstNonEmpty(md.Title, title)
		chapters = append(chapters, &Chapter{
			ID:         shaID(comic.ID, "chapter", archive.RelPath),
			ComicID:    comic.ID,
			Title:      title,
			SourceType: "archive",
			SourceRef:  cleanRel(archive.RelPath),
			PageCount:  count,
		})
	}
	sort.Slice(chapters, func(i, j int) bool { return naturalLess(chapters[i].Title, chapters[j].Title) })
	for i, chapter := range chapters {
		chapter.Index = i + 1
	}
	comic.Chapters = chapters
	if len(chapters) == 0 {
		return nil, errors.New("empty series")
	}
	comic.UpdatedAt = time.Now()
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func applyMetadata(comic *Comic, meta ParsedMetadata) {
	comic.Title = firstNonEmpty(meta.Title, comic.Title)
	comic.Subtitle = firstNonEmpty(meta.Subtitle, firstFrom(meta.Authors))
	comic.Description = firstNonEmpty(meta.Description, comic.Description)
	comic.Authors = uniqueStrings(meta.Authors)
	comic.Tags = uniqueStrings(meta.Tags)
	comic.Language = meta.Language
}

func latestMod(items []Entry) time.Time {
	var out time.Time
	for _, item := range items {
		if item.ModTime.After(out) {
			out = item.ModTime
		}
	}
	if out.IsZero() {
		out = time.Now()
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstFrom(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func splitCSVish(value string) []string {
	value = strings.NewReplacer(";", ",", "|", ",", "/", ",").Replace(value)
	return uniqueStrings(strings.Split(value, ","))
}

func (a *App) loadMetadataForDir(ctx context.Context, backend Backend, rel string, fallbackTitle string) (ParsedMetadata, error) {
	meta := ParsedMetadata{Title: fallbackTitle}
	if a.cfg.Metadata.ReadComicInfo {
		if raw, err := backend.ReadSmallFile(ctx, relJoin(rel, "ComicInfo.xml"), 256*1024); err == nil {
			applyComicInfo(&meta, raw)
		}
	}
	if a.cfg.Metadata.ReadSidecar {
		if raw, err := backend.ReadSmallFile(ctx, relJoin(rel, ".venera.json"), 256*1024); err == nil {
			applySidecar(&meta, raw)
		}
	}
	return meta, nil
}

func (a *App) loadMetadataForArchive(ctx context.Context, backend Backend, rel string, fallbackTitle string) (ParsedMetadata, int, error) {
	meta := ParsedMetadata{Title: fallbackTitle}
	archive, err := openArchive(ctx, backend, rel, a.cfg.Server.CacheDir)
	if err != nil {
		return meta, 0, err
	}
	defer archive.Close()

	pageCount := 0
	entries := archive.Entries()
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if a.cfg.Metadata.ReadComicInfo && strings.EqualFold(path.Base(entry.Name), "ComicInfo.xml") {
			rc, err := archive.Open(ctx, entry.Name)
			if err == nil {
				raw, _ := io.ReadAll(io.LimitReader(rc, 256*1024))
				_ = rc.Close()
				applyComicInfo(&meta, raw)
			}
		}
		if isImageFile(entry.Name) {
			pageCount++
		}
	}
	if a.cfg.Metadata.ReadSidecar {
		if raw, err := backend.ReadSmallFile(ctx, rel+".venera.json", 256*1024); err == nil {
			applySidecar(&meta, raw)
		}
	}
	return meta, pageCount, nil
}

func applyComicInfo(meta *ParsedMetadata, raw []byte) {
	var info comicInfoXML
	if xml.Unmarshal(raw, &info) != nil {
		return
	}
	meta.Title = firstNonEmpty(info.Title, info.Series, meta.Title)
	meta.Description = firstNonEmpty(info.Summary, meta.Description)
	meta.Authors = uniqueStrings(append(meta.Authors, splitCSVish(info.Writer)...))
	meta.Authors = uniqueStrings(append(meta.Authors, splitCSVish(info.Penciller)...))
	meta.Tags = uniqueStrings(append(meta.Tags, splitCSVish(info.Genre)...))
	meta.Language = firstNonEmpty(info.LanguageISO, meta.Language)
}

func applySidecar(meta *ParsedMetadata, raw []byte) {
	var override ParsedMetadata
	if json.Unmarshal(raw, &override) != nil {
		return
	}
	meta.Title = firstNonEmpty(override.Title, meta.Title)
	meta.Subtitle = firstNonEmpty(override.Subtitle, meta.Subtitle)
	meta.Description = firstNonEmpty(override.Description, meta.Description)
	if len(override.Authors) > 0 {
		meta.Authors = uniqueStrings(override.Authors)
	}
	if len(override.Tags) > 0 {
		meta.Tags = uniqueStrings(override.Tags)
	}
	meta.Language = firstNonEmpty(override.Language, meta.Language)
}

func (a *App) materializeChapterPages(ctx context.Context, chapter *Chapter) ([]PageRef, error) {
	if len(chapter.pages) > 0 {
		return chapter.pages, nil
	}
	comic := a.comicByID(chapter.ComicID)
	if comic == nil {
		return nil, fmt.Errorf("comic not found")
	}
	backend := a.backends[comic.LibraryID]
	switch chapter.SourceType {
	case "dir":
		entries, err := backend.ListDir(ctx, chapter.SourceRef)
		if err != nil {
			return nil, err
		}
		pages := []PageRef{}
		for _, entry := range entries {
			if !entry.IsDir && isImageFile(entry.Name) {
				pages = append(pages, PageRef{PageIndex: len(pages), SourceType: "file", SourceRef: entry.RelPath, Name: entry.Name})
			}
		}
		chapter.pages = pages
	case "archive":
		archive, err := openArchive(ctx, backend, chapter.SourceRef, a.cfg.Server.CacheDir)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		pages := []PageRef{}
		for _, entry := range archive.Entries() {
			if entry.IsDir || !isImageFile(entry.Name) {
				continue
			}
			pages = append(pages, PageRef{PageIndex: len(pages), SourceType: "archive", SourceRef: chapter.SourceRef, EntryName: entry.Name, Name: path.Base(entry.Name)})
		}
		sort.Slice(pages, func(i, j int) bool { return naturalLess(pages[i].Name, pages[j].Name) })
		for i := range pages {
			pages[i].PageIndex = i
		}
		chapter.pages = pages
	default:
		return nil, fmt.Errorf("unsupported chapter source type: %s", chapter.SourceType)
	}
	chapter.PageCount = len(chapter.pages)
	return chapter.pages, nil
}

func (a *App) comicByID(id string) *Comic {
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	return a.comics[id]
}
