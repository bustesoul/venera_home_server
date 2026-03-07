package app

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

	archivepkg "venera_home_server/archive"
	backendpkg "venera_home_server/backend"
	configpkg "venera_home_server/config"
	favoritespkg "venera_home_server/favorites"
	"venera_home_server/shared"
)

type App struct {
	cfg       *configpkg.Config
	backends  map[string]backendpkg.Backend
	comicsMu  sync.RWMutex
	comics    map[string]*Comic
	libraries map[string][]string
	chapters  map[string]*Chapter
	favorites *favoritespkg.FavoritesStore
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

func NewApp(cfg *configpkg.Config) (*App, error) {
	if err := shared.EnsureDir(cfg.Server.DataDir); err != nil {
		return nil, err
	}
	if err := shared.EnsureDir(cfg.Server.CacheDir); err != nil {
		return nil, err
	}
	favorites, err := favoritespkg.LoadFavoritesStore(cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	app := &App{
		cfg:       cfg,
		backends:  map[string]backendpkg.Backend{},
		comics:    map[string]*Comic{},
		libraries: map[string][]string{},
		chapters:  map[string]*Chapter{},
		favorites: favorites,
	}
	for _, lib := range cfg.Libraries {
		var backend backendpkg.Backend
		switch lib.Kind {
		case "local":
			backend = backendpkg.NewLocalBackend(lib.Root)
		case "smb":
			backend, err = backendpkg.NewSMBBackend(lib)
		case "webdav":
			backend, err = backendpkg.NewWebDAVBackend(lib, cfg.Server.CacheDir)
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

func (a *App) libraryConfig(id string) (configpkg.LibraryConfig, bool) {
	for _, lib := range a.cfg.Libraries {
		if lib.ID == id {
			return lib, true
		}
	}
	return configpkg.LibraryConfig{}, false
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
			return shared.NaturalLess(left.Title, right.Title)
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

func (a *App) effectiveScanMode(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string) string {
	mode := normalizeScanMode(lib.ScanMode)
	if !a.cfg.Metadata.ReadSidecar {
		return mode
	}
	raw, err := backend.ReadSmallFile(ctx, shared.RelJoin(rel, ".venera.json"), 64*1024)
	if err != nil {
		return mode
	}
	var override ParsedMetadata
	if json.Unmarshal(raw, &override) != nil || strings.TrimSpace(override.ScanMode) == "" {
		return mode
	}
	return normalizeScanMode(override.ScanMode)
}

func (a *App) scanLibrary(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend) ([]*Comic, error) {
	return a.scanDir(ctx, lib, backend, "")
}

func (a *App) scanDir(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string) ([]*Comic, error) {
	entries, err := backend.ListDir(ctx, rel)
	if err != nil {
		return nil, err
	}
	dirMeta, _ := a.loadMetadataForDir(ctx, backend, rel, shared.BaseNameTitle(rel))
	if dirMeta.Hidden {
		return []*Comic{}, nil
	}
	var images, archives, dirs []backendpkg.Entry
	for _, entry := range entries {
		if entry.IsDir {
			dirs = append(dirs, entry)
		} else if shared.IsImageFile(entry.Name) {
			images = append(images, entry)
		} else if shared.IsArchiveFile(entry.Name) {
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
	chapterDirs := make([]backendpkg.Entry, 0, len(dirs))
	for _, dir := range dirs {
		ok, err := a.dirLooksLikeChapter(ctx, backend, dir.RelPath)
		if err == nil && ok {
			chapterDirs = append(chapterDirs, dir)
		}
	}
	if scanMode == "auto" && rel != "" && len(dirs) == len(chapterDirs) && len(archives)+len(chapterDirs) >= 2 {
		parentMeta := dirMeta
		candidates := make([]chapterCandidate, 0, len(archives)+len(chapterDirs))
		for _, dir := range chapterDirs {
			candidate, err := a.inspectChapterDirCandidate(ctx, backend, dir.RelPath)
			if err == nil && !candidate.Meta.Hidden {
				candidates = append(candidates, candidate)
			}
		}
		for _, archive := range archives {
			candidate, err := a.inspectArchiveCandidate(ctx, backend, archive.RelPath, shared.BaseNameTitle(archive.Name))
			if err == nil && !candidate.Meta.Hidden {
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
		comic, err := a.buildArchiveComic(ctx, lib, backend, archives[0].RelPath, shared.BaseNameTitle(archives[0].Name))
		if err != nil {
			return nil, err
		}
		if comic == nil {
			return []*Comic{}, nil
		}
		return []*Comic{comic}, nil
	}

	if len(dirs) == 1 && len(archives) == 0 && len(entries) == 1 {
		return a.scanDir(ctx, lib, backend, dirs[0].RelPath)
	}

	out := []*Comic{}
	for _, archive := range archives {
		comic, err := a.buildArchiveComic(ctx, lib, backend, archive.RelPath, shared.BaseNameTitle(archive.Name))
		if err == nil && comic != nil {
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

func (a *App) dirLooksLikeChapter(ctx context.Context, backend backendpkg.Backend, rel string) (bool, error) {
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
		} else if shared.IsImageFile(entry.Name) {
			imageCount++
		} else if shared.IsArchiveFile(entry.Name) {
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

func (a *App) buildBaseComic(lib configpkg.LibraryConfig, rel string, title string, rootType string) *Comic {
	if title == "" {
		title = shared.BaseNameTitle(rel)
	}
	now := time.Now()
	return &Comic{
		ID:          shared.SHAID(lib.ID, rootType, rel),
		LibraryID:   lib.ID,
		LibraryName: lib.Name,
		Storage:     lib.Kind,
		Title:       title,
		AddedAt:     now,
		UpdatedAt:   now,
		RootType:    rootType,
		RootRef:     shared.CleanRel(rel),
		SourceURL:   shared.CleanRel(rel),
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
		out.Authors = shared.UniqueStrings(override.Authors)
	} else {
		out.Authors = shared.UniqueStrings(out.Authors)
	}
	if len(override.Tags) > 0 {
		out.Tags = shared.UniqueStrings(override.Tags)
	} else {
		out.Tags = shared.UniqueStrings(out.Tags)
	}
	out.Language = firstNonEmpty(override.Language, out.Language)
	out.ScanMode = firstNonEmpty(override.ScanMode, out.ScanMode)
	out.Hidden = out.Hidden || override.Hidden
	out.hasExplicitTitle = out.hasExplicitTitle || override.hasExplicitTitle
	out.hasExplicitSeries = out.hasExplicitSeries || override.hasExplicitSeries
	return out
}

func metadataCompatible(left ParsedMetadata, right ParsedMetadata) bool {
	if left.Language != "" && right.Language != "" && !strings.EqualFold(left.Language, right.Language) {
		return false
	}
	if len(left.Authors) > 0 && len(right.Authors) > 0 && !shared.ShareAnyFold(left.Authors, right.Authors) {
		return false
	}
	return true
}

func (a *App) inspectChapterDirCandidate(ctx context.Context, backend backendpkg.Backend, rel string) (chapterCandidate, error) {
	fallback := shared.BaseNameTitle(rel)
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
		if shared.IsImageFile(item.Name) {
			count++
		}
		if shared.IsArchiveFile(item.Name) {
			archiveRef = item.RelPath
			archiveName = item.Name
		}
	}
	sourceType := "dir"
	sourceRef := shared.CleanRel(rel)
	meta := dirMeta
	if count == 0 && archiveRef != "" && dirCount == 0 {
		sourceType = "archive"
		sourceRef = shared.CleanRel(archiveRef)
		archiveMeta, count2, err := a.loadMetadataForArchive(ctx, backend, archiveRef, shared.BaseNameTitle(archiveName))
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

func (a *App) inspectArchiveCandidate(ctx context.Context, backend backendpkg.Backend, rel string, fallback string) (chapterCandidate, error) {
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
		SourceRef:    shared.CleanRel(rel),
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

func (a *App) buildSeriesComicFromCandidates(lib configpkg.LibraryConfig, rel string, groupKey string, meta ParsedMetadata, candidates []chapterCandidate) (*Comic, error) {
	if len(candidates) == 0 {
		return nil, errors.New("empty series")
	}
	title := resolveComicTitle(meta, shared.BaseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, title, "series")
	comic.ID = shared.SHAID(lib.ID, "series", rel, groupKey)
	applyMetadata(comic, meta)
	sort.Slice(candidates, func(i, j int) bool { return shared.NaturalLess(candidates[i].SortKey, candidates[j].SortKey) })
	chapters := make([]*Chapter, 0, len(candidates))
	for i, candidate := range candidates {
		chapters = append(chapters, &Chapter{
			ID:          shared.SHAID(comic.ID, "chapter", candidate.SourceRef, candidate.EntryPrefix),
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

func (a *App) detectArchiveFolderGroups(ctx context.Context, backend backendpkg.Backend, rel string) ([]archiveFolderGroup, error) {
	archive, err := archivepkg.Open(ctx, backend, rel, a.cfg.Server.CacheDir)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	topLevelImages := 0
	counts := map[string]int{}
	for _, entry := range archive.Entries() {
		if entry.IsDir || !shared.IsImageFile(entry.Name) {
			continue
		}
		name := shared.CleanRel(strings.ReplaceAll(entry.Name, "\\", "/"))
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
		groups = append(groups, archiveFolderGroup{Prefix: prefix, Title: shared.BaseNameTitle(prefix), PageCount: count})
	}
	sort.Slice(groups, func(i, j int) bool { return shared.NaturalLess(groups[i].Title, groups[j].Title) })
	return groups, nil
}

func (a *App) buildArchiveSeriesComic(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string, fallbackTitle string, meta ParsedMetadata) (*Comic, error) {
	groups, err := a.detectArchiveFolderGroups(ctx, backend, rel)
	if err != nil || len(groups) < 2 {
		return nil, err
	}
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, fallbackTitle), "archive")
	applyMetadata(comic, meta)
	chapters := make([]*Chapter, 0, len(groups))
	for i, group := range groups {
		chapters = append(chapters, &Chapter{
			ID:          shared.SHAID(comic.ID, "chapter", rel, group.Prefix),
			ComicID:     comic.ID,
			Title:       group.Title,
			Index:       i + 1,
			SourceType:  "archive",
			SourceRef:   shared.CleanRel(rel),
			EntryPrefix: shared.CleanRel(group.Prefix),
			PageCount:   group.PageCount,
		})
	}
	comic.Chapters = chapters
	comic.UpdatedAt = time.Now()
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) buildDirComic(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string, images []backendpkg.Entry) (*Comic, error) {
	meta, _ := a.loadMetadataForDir(ctx, backend, rel, shared.BaseNameTitle(rel))
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, shared.BaseNameTitle(rel)), "dir")
	applyMetadata(comic, meta)
	chapter := &Chapter{
		ID:         shared.SHAID(comic.ID, "chapter", rel),
		ComicID:    comic.ID,
		Title:      comic.Title,
		Index:      1,
		SourceType: "dir",
		SourceRef:  shared.CleanRel(rel),
		PageCount:  len(images),
	}
	comic.Chapters = []*Chapter{chapter}
	comic.UpdatedAt = latestMod(images)
	comic.AddedAt = comic.UpdatedAt
	return comic, nil
}

func (a *App) buildArchiveComic(ctx context.Context, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string, fallbackTitle string) (*Comic, error) {
	meta, pageCount, err := a.loadMetadataForArchive(ctx, backend, rel, fallbackTitle)
	if err != nil {
		return nil, err
	}
	if meta.Hidden {
		return nil, nil
	}
	if comic, err := a.buildArchiveSeriesComic(ctx, lib, backend, rel, fallbackTitle, meta); err == nil && comic != nil {
		return comic, nil
	}
	comic := a.buildBaseComic(lib, rel, resolveComicTitle(meta, fallbackTitle), "archive")
	applyMetadata(comic, meta)
	chapter := &Chapter{
		ID:         shared.SHAID(comic.ID, "chapter", rel),
		ComicID:    comic.ID,
		Title:      comic.Title,
		Index:      1,
		SourceType: "archive",
		SourceRef:  shared.CleanRel(rel),
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
	comic.Authors = shared.UniqueStrings(meta.Authors)
	comic.Tags = shared.UniqueStrings(meta.Tags)
	comic.Language = meta.Language
}

func latestMod(items []backendpkg.Entry) time.Time {
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
	return shared.UniqueStrings(strings.Split(value, ","))
}

func (a *App) loadMetadataForDir(ctx context.Context, backend backendpkg.Backend, rel string, fallbackTitle string) (ParsedMetadata, error) {
	meta := ParsedMetadata{Title: fallbackTitle}
	if a.cfg.Metadata.ReadComicInfo {
		if raw, err := backend.ReadSmallFile(ctx, shared.RelJoin(rel, "ComicInfo.xml"), 256*1024); err == nil {
			applyComicInfo(&meta, raw)
		}
	}
	if a.cfg.Metadata.ReadSidecar {
		if raw, err := backend.ReadSmallFile(ctx, shared.RelJoin(rel, ".venera.json"), 256*1024); err == nil {
			applySidecar(&meta, raw)
		}
	}
	return meta, nil
}

func (a *App) loadMetadataForArchive(ctx context.Context, backend backendpkg.Backend, rel string, fallbackTitle string) (ParsedMetadata, int, error) {
	meta := ParsedMetadata{Title: fallbackTitle}
	archive, err := archivepkg.Open(ctx, backend, rel, a.cfg.Server.CacheDir)
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
		if shared.IsImageFile(entry.Name) {
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
	meta.Authors = shared.UniqueStrings(append(meta.Authors, splitCSVish(info.Writer)...))
	meta.Authors = shared.UniqueStrings(append(meta.Authors, splitCSVish(info.Penciller)...))
	meta.Tags = shared.UniqueStrings(append(meta.Tags, splitCSVish(info.Genre)...))
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
		meta.Authors = shared.UniqueStrings(override.Authors)
	}
	if len(override.Tags) > 0 {
		meta.Tags = shared.UniqueStrings(override.Tags)
	}
	meta.Language = firstNonEmpty(override.Language, meta.Language)
	meta.ScanMode = firstNonEmpty(override.ScanMode, meta.ScanMode)
	meta.Hidden = meta.Hidden || override.Hidden
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
			if !entry.IsDir && shared.IsImageFile(entry.Name) {
				pages = append(pages, PageRef{PageIndex: len(pages), SourceType: "file", SourceRef: entry.RelPath, Name: entry.Name, Size: entry.Size, ModTime: entry.ModTime})
			}
		}
		chapter.pages = pages
	case "archive":
		archive, err := archivepkg.Open(ctx, backend, chapter.SourceRef, a.cfg.Server.CacheDir)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		pages := []PageRef{}
		prefix := shared.CleanRel(chapter.EntryPrefix)
		for _, entry := range archive.Entries() {
			if entry.IsDir || !shared.IsImageFile(entry.Name) {
				continue
			}
			entryName := shared.CleanRel(strings.ReplaceAll(entry.Name, "\\", "/"))
			if prefix != "" && entryName != prefix && !strings.HasPrefix(entryName, prefix+"/") {
				continue
			}
			pages = append(pages, PageRef{PageIndex: len(pages), SourceType: "archive", SourceRef: chapter.SourceRef, EntryName: entry.Name, Name: path.Base(entryName), Size: entry.Size, ModTime: entry.ModTime})
		}
		sort.Slice(pages, func(i, j int) bool { return shared.NaturalLess(pages[i].Name, pages[j].Name) })
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
