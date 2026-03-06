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

func normalizeScanMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return "auto"
	case "flat":
		return "flat"
	default:
		return "auto"
	}
}

func (a *App) effectiveScanMode(ctx context.Context, lib LibraryConfig, backend Backend, rel string) string {
	mode := normalizeScanMode(lib.ScanMode)
	if !a.cfg.Metadata.ReadSidecar {
		return mode
	}
	raw, err := backend.ReadSmallFile(ctx, relJoin(rel, ".venera.json"), 64*1024)
	if err != nil {
		return mode
	}
	var override ParsedMetadata
	if json.Unmarshal(raw, &override) != nil || strings.TrimSpace(override.ScanMode) == "" {
		return mode
	}
	return normalizeScanMode(override.ScanMode)
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

	scanMode := a.effectiveScanMode(ctx, lib, backend, rel)
	chapterDirs := make([]Entry, 0, len(dirs))
	for _, dir := range dirs {
		ok, err := a.dirLooksLikeChapter(ctx, backend, dir.RelPath)
		if err == nil && ok {
			chapterDirs = append(chapterDirs, dir)
		}
	}
	if scanMode == "auto" && rel != "" && len(dirs) == len(chapterDirs) && len(archives)+len(chapterDirs) >= 2 {
		parentMeta, _ := a.loadMetadataForDir(ctx, backend, rel, baseNameTitle(rel))
		candidates := make([]chapterCandidate, 0, len(archives)+len(chapterDirs))
		for _, dir := range chapterDirs {
			candidate, err := a.inspectChapterDirCandidate(ctx, backend, dir.RelPath)
			if err == nil {
				candidates = append(candidates, candidate)
			}
		}
		for _, archive := range archives {
			candidate, err := a.inspectArchiveCandidate(ctx, backend, archive.RelPath, baseNameTitle(archive.Name))
			if err == nil {
				candidates = append(candidates, candidate)
			}
		}
		if meta, groupKey, ok := shouldBuildSeries(parentMeta, candidates); ok {
			comic, err := a.buildSeriesComicFromCandidates(lib, rel, groupKey, meta, candidates)
			if err == nil {
				return []*Comic{comic}, nil
			}
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

type chapterCandidate struct {
	Meta         ParsedMetadata
	GroupKey     string
	GroupTitle   string
	ChapterTitle string
	SourceType   string
	SourceRef    string
	EntryPrefix  string
	PageCount    int
	SortKey      string
}

type archiveFolderGroup struct {
	Prefix    string
	Title     string
	PageCount int
}

func normalizeMetaKey(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func explicitSeriesKey(meta ParsedMetadata) string {
	switch {
	case meta.hasExplicitSeries && strings.TrimSpace(meta.Series) != "":
		return normalizeMetaKey(meta.Series)
	case meta.hasExplicitTitle && strings.TrimSpace(meta.Title) != "":
		return normalizeMetaKey(meta.Title)
	default:
		return ""
	}
}

func resolveComicTitle(meta ParsedMetadata, fallback string) string {
	return firstNonEmpty(meta.Series, meta.Title, fallback)
}

func resolveChapterTitle(meta ParsedMetadata, fallback string, groupTitle string) string {
	title := firstNonEmpty(meta.Title, fallback)
	if normalizeMetaKey(title) != "" && normalizeMetaKey(title) == normalizeMetaKey(groupTitle) {
		return fallback
	}
	return title
}

func mergeMetadata(base ParsedMetadata, override ParsedMetadata) ParsedMetadata {
	out := base
	out.Title = firstNonEmpty(override.Title, out.Title)
	out.Series = firstNonEmpty(override.Series, out.Series)
	out.Subtitle = firstNonEmpty(override.Subtitle, out.Subtitle)
	out.Description = firstNonEmpty(override.Description, out.Description)
	if len(override.Authors) > 0 {
		out.Authors = uniqueStrings(override.Authors)
	} else {
		out.Authors = uniqueStrings(out.Authors)
	}
	if len(override.Tags) > 0 {
		out.Tags = uniqueStrings(override.Tags)
	} else {
		out.Tags = uniqueStrings(out.Tags)
	}
	out.Language = firstNonEmpty(override.Language, out.Language)
	out.ScanMode = firstNonEmpty(override.ScanMode, out.ScanMode)
	out.hasExplicitTitle = out.hasExplicitTitle || override.hasExplicitTitle
	out.hasExplicitSeries = out.hasExplicitSeries || override.hasExplicitSeries
	return out
}

func metadataCompatible(left ParsedMetadata, right ParsedMetadata) bool {
	if left.Language != "" && right.Language != "" && !strings.EqualFold(left.Language, right.Language) {
		return false
	}
	if len(left.Authors) > 0 && len(right.Authors) > 0 && !shareAny(left.Authors, right.Authors) {
		return false
	}
	return true
}

func (a *App) inspectChapterDirCandidate(ctx context.Context, backend Backend, rel string) (chapterCandidate, error) {
	fallback := baseNameTitle(rel)
	dirMeta, _ := a.loadMetadataForDir(ctx, backend, rel, fallback)
	entries, err := backend.ListDir(ctx, rel)
	if err != nil {
		return chapterCandidate{}, err
	}
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
	sourceRef := cleanRel(rel)
	meta := dirMeta
	if count == 0 && archiveRef != "" && dirCount == 0 {
		sourceType = "archive"
		sourceRef = cleanRel(archiveRef)
		archiveMeta, count2, err := a.loadMetadataForArchive(ctx, backend, archiveRef, baseNameTitle(archiveName))
		if err == nil {
			meta = mergeMetadata(archiveMeta, dirMeta)
			count = count2
		}
	}
	groupTitle := resolveComicTitle(meta, fallback)
	return chapterCandidate{
		Meta:         meta,
		GroupKey:     explicitSeriesKey(meta),
		GroupTitle:   groupTitle,
		ChapterTitle: resolveChapterTitle(meta, fallback, groupTitle),
		SourceType:   sourceType,
		SourceRef:    sourceRef,
		PageCount:    count,
		SortKey:      fallback,
	}, nil
}

func (a *App) inspectArchiveCandidate(ctx context.Context, backend Backend, rel string, fallback string) (chapterCandidate, error) {
	meta, pageCount, err := a.loadMetadataForArchive(ctx, backend, rel, fallback)
	if err != nil {
		return chapterCandidate{}, err
	}
	groupTitle := resolveComicTitle(meta, fallback)
	return chapterCandidate{
		Meta:         meta,
		GroupKey:     explicitSeriesKey(meta),
		GroupTitle:   groupTitle,
		ChapterTitle: resolveChapterTitle(meta, fallback, groupTitle),
		SourceType:   "archive",
		SourceRef:    cleanRel(rel),
		PageCount:    pageCount,
		SortKey:      fallback,
	}, nil
}

func shouldBuildSeries(parentMeta ParsedMetadata, candidates []chapterCandidate) (ParsedMetadata, string, bool) {
	if len(candidates) < 2 {
		return ParsedMetadata{}, "", false
	}
	parentKey := explicitSeriesKey(parentMeta)
	if parentKey != "" {
		for _, candidate := range candidates {
			if candidate.GroupKey != "" && candidate.GroupKey != parentKey {
				return ParsedMetadata{}, "", false
			}
			if !metadataCompatible(parentMeta, candidate.Meta) {
				return ParsedMetadata{}, "", false
			}
		}
		return parentMeta, parentKey, true
	}
	rootMeta := candidates[0].Meta
	key := candidates[0].GroupKey
	if key == "" {
		return ParsedMetadata{}, "", false
	}
	for _, candidate := range candidates[1:] {
		if candidate.GroupKey == "" || candidate.GroupKey != key {
			return ParsedMetadata{}, "", false
		}
		if !metadataCompatible(rootMeta, candidate.Meta) {
			return ParsedMetadata{}, "", false
		}
	}
	return rootMeta, key, true
}

func (a *App) buildSeriesComicFromCandidates(lib LibraryConfig, rel string, groupKey string, meta ParsedMetadata, candidates []chapterCandidate) (*Comic, error) {
	if len(candidates) == 0 {
		return nil, errors.New("empty series")
	}
	title := resolveComicTitle(meta, baseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, title, "series")
	comic.ID = shaID(lib.ID, "series", rel, groupKey)
	applyMetadata(comic, meta)
	sort.Slice(candidates, func(i, j int) bool { return naturalLess(candidates[i].SortKey, candidates[j].SortKey) })
	chapters := make([]*Chapter, 0, len(candidates))
	for i, candidate := range candidates {
		chapters = append(chapters, &Chapter{
			ID:          shaID(comic.ID, "chapter", candidate.SourceRef, candidate.EntryPrefix),
			ComicID:     comic.ID,
			Title:       candidate.ChapterTitle,
			Index:       i + 1,
			SourceType:  candidate.SourceType,
			SourceRef:   candidate.SourceRef,
			EntryPrefix: candidate.EntryPrefix,
			PageCount:   candidate.PageCount,
		})
	}
	comic.Chapters = chapters
	comic.UpdatedAt = time.Now()
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) detectArchiveFolderGroups(ctx context.Context, backend Backend, rel string) ([]archiveFolderGroup, error) {
	archive, err := openArchive(ctx, backend, rel, a.cfg.Server.CacheDir)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	topLevelImages := 0
	counts := map[string]int{}
	for _, entry := range archive.Entries() {
		if entry.IsDir || !isImageFile(entry.Name) {
			continue
		}
		name := cleanRel(strings.ReplaceAll(entry.Name, "\\", "/"))
		parts := strings.Split(name, "/")
		if len(parts) <= 1 {
			topLevelImages++
			continue
		}
		counts[parts[0]]++
	}
	if topLevelImages > 0 || len(counts) < 2 {
		return nil, nil
	}
	groups := make([]archiveFolderGroup, 0, len(counts))
	for prefix, count := range counts {
		groups = append(groups, archiveFolderGroup{Prefix: prefix, Title: baseNameTitle(prefix), PageCount: count})
	}
	sort.Slice(groups, func(i, j int) bool { return naturalLess(groups[i].Title, groups[j].Title) })
	return groups, nil
}

func (a *App) buildArchiveSeriesComic(ctx context.Context, lib LibraryConfig, backend Backend, rel string, fallbackTitle string, meta ParsedMetadata) (*Comic, error) {
	groups, err := a.detectArchiveFolderGroups(ctx, backend, rel)
	if err != nil || len(groups) < 2 {
		return nil, err
	}
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, fallbackTitle), "archive")
	applyMetadata(comic, meta)
	chapters := make([]*Chapter, 0, len(groups))
	for i, group := range groups {
		chapters = append(chapters, &Chapter{
			ID:          shaID(comic.ID, "chapter", rel, group.Prefix),
			ComicID:     comic.ID,
			Title:       group.Title,
			Index:       i + 1,
			SourceType:  "archive",
			SourceRef:   cleanRel(rel),
			EntryPrefix: cleanRel(group.Prefix),
			PageCount:   group.PageCount,
		})
	}
	comic.Chapters = chapters
	comic.UpdatedAt = time.Now()
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) buildDirComic(ctx context.Context, lib LibraryConfig, backend Backend, rel string, images []Entry) (*Comic, error) {
	meta, _ := a.loadMetadataForDir(ctx, backend, rel, baseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, baseNameTitle(rel)), "dir")
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
	if comic, err := a.buildArchiveSeriesComic(ctx, lib, backend, rel, fallbackTitle, meta); err == nil && comic != nil {
		return comic, nil
	}
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, fallbackTitle), "archive")
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

func applyMetadata(comic *Comic, meta ParsedMetadata) {
	comic.Title = firstNonEmpty(meta.Series, meta.Title, comic.Title)
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
	if strings.TrimSpace(info.Title) != "" {
		meta.Title = strings.TrimSpace(info.Title)
		meta.hasExplicitTitle = true
	}
	if strings.TrimSpace(info.Series) != "" {
		meta.Series = strings.TrimSpace(info.Series)
		meta.hasExplicitSeries = true
	}
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
	if strings.TrimSpace(override.Title) != "" {
		meta.Title = strings.TrimSpace(override.Title)
		meta.hasExplicitTitle = true
	}
	if strings.TrimSpace(override.Series) != "" {
		meta.Series = strings.TrimSpace(override.Series)
		meta.hasExplicitSeries = true
	}
	meta.Subtitle = firstNonEmpty(override.Subtitle, meta.Subtitle)
	meta.Description = firstNonEmpty(override.Description, meta.Description)
	if len(override.Authors) > 0 {
		meta.Authors = uniqueStrings(override.Authors)
	}
	if len(override.Tags) > 0 {
		meta.Tags = uniqueStrings(override.Tags)
	}
	meta.Language = firstNonEmpty(override.Language, meta.Language)
	meta.ScanMode = firstNonEmpty(override.ScanMode, meta.ScanMode)
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
		prefix := cleanRel(chapter.EntryPrefix)
		for _, entry := range archive.Entries() {
			if entry.IsDir || !isImageFile(entry.Name) {
				continue
			}
			entryName := cleanRel(strings.ReplaceAll(entry.Name, "\\", "/"))
			if prefix != "" && entryName != prefix && !strings.HasPrefix(entryName, prefix+"/") {
				continue
			}
			pages = append(pages, PageRef{PageIndex: len(pages), SourceType: "archive", SourceRef: chapter.SourceRef, EntryName: entry.Name, Name: path.Base(entryName)})
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
