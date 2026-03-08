package app

import (
	"context"
	"html"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	archivepkg "venera_home_server/archive"
	backendpkg "venera_home_server/backend"
	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

type metadataScanStartedKey struct{}

var ehIDPattern = regexp.MustCompile(`(?i)\[?EH-?(\d+)\]?`)
var ehURLPattern = regexp.MustCompile(`(?i)g/(\d+)/([0-9a-f]+)`)
var pixivIDPattern = regexp.MustCompile(`\d{8,10}`)
var keywordSplitPattern = regexp.MustCompile(`[^\p{L}\p{Nd}]+`)

func withMetadataScanStarted(ctx context.Context, seenAt time.Time) context.Context {
	return context.WithValue(ctx, metadataScanStartedKey{}, seenAt.UTC())
}

func metadataSeenAt(ctx context.Context) time.Time {
	if ctx != nil {
		if value, ok := ctx.Value(metadataScanStartedKey{}).(time.Time); ok && !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Now().UTC()
}

func (a *App) mergeMetadataFromStore(ctx context.Context, base ParsedMetadata, input metadatapkg.ScanInput) ParsedMetadata {
	if a.metadataStore == nil || !input.Locator.Valid() {
		return base
	}
	record, err := a.metadataStore.UpsertScanned(ctx, input, metadataSeenAt(ctx))
	if err != nil || record == nil {
		return base
	}
	return mergeStoredMetadata(base, record)
}

func mergeStoredMetadata(base ParsedMetadata, record *metadatapkg.Record) ParsedMetadata {
	if record == nil {
		return base
	}
	out := base
	if strings.TrimSpace(out.Title) == "" || !out.hasExplicitTitle {
		out.Title = firstNonEmpty(record.Title, record.TitleJPN, out.Title)
	}
	if strings.TrimSpace(out.Subtitle) == "" {
		out.Subtitle = firstNonEmpty(record.Subtitle, out.Subtitle)
	}
	if strings.TrimSpace(out.Description) == "" {
		out.Description = firstNonEmpty(record.Description, out.Description)
	}
	if len(out.Authors) == 0 && len(record.Artists) > 0 {
		out.Authors = shared.UniqueStrings(append([]string(nil), record.Artists...))
	}
	if len(out.Tags) == 0 && len(record.Tags) > 0 {
		out.Tags = shared.UniqueStrings(append([]string(nil), record.Tags...))
	}
	if strings.TrimSpace(out.Language) == "" {
		out.Language = firstNonEmpty(record.Language, out.Language)
	}
	if strings.TrimSpace(out.SourceURL) == "" {
		out.SourceURL = firstNonEmpty(record.SourceURL, out.SourceURL)
	}
	return out
}

func metadataFolderPath(lib configpkg.LibraryConfig, rootRef string) string {
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

func metadataHintForRoot(rootRef string, fallbackTitle string) metadatapkg.Hint {
	target := html.UnescapeString(shared.CleanRel(rootRef))
	fallbackTitle = strings.TrimSpace(html.UnescapeString(fallbackTitle))
	if fallbackTitle != "" {
		if target != "" {
			target += " "
		}
		target += fallbackTitle
	}
	hint := metadatapkg.Hint{}
	if match := ehURLPattern.FindStringSubmatch(target); len(match) == 3 {
		hint.EHGalleryID = match[1]
		hint.EHToken = strings.ToLower(match[2])
	} else if match := ehIDPattern.FindStringSubmatch(target); len(match) == 2 {
		hint.EHGalleryID = match[1]
	}
	pixivSeen := map[string]bool{}
	for _, match := range pixivIDPattern.FindAllString(target, -1) {
		if pixivSeen[match] {
			continue
		}
		pixivSeen[match] = true
		hint.PixivIllustIDs = append(hint.PixivIllustIDs, match)
	}
	keywordSeen := map[string]bool{}
	for _, part := range keywordSplitPattern.Split(strings.ToLower(target), -1) {
		part = strings.TrimSpace(part)
		if len(part) < 3 {
			continue
		}
		if _, err := strconv.Atoi(part); err == nil {
			continue
		}
		if keywordSeen[part] {
			continue
		}
		keywordSeen[part] = true
		hint.Keywords = append(hint.Keywords, part)
		if len(hint.Keywords) >= 8 {
			break
		}
	}
	return hint
}

func fingerprintFromParts(parts []string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		normalized = append(normalized, part)
	}
	if len(normalized) == 0 {
		return ""
	}
	return shared.SHAID(normalized...)
}

func dirContentFingerprint(images []backendpkg.Entry) string {
	if len(images) == 0 {
		return ""
	}
	ordered := append([]backendpkg.Entry(nil), images...)
	sort.Slice(ordered, func(i, j int) bool { return shared.NaturalLess(ordered[i].Name, ordered[j].Name) })
	parts := []string{"dir", strconv.Itoa(len(ordered))}
	for _, item := range sampleEntries(ordered) {
		parts = append(parts, item.Name, strconv.FormatInt(item.Size, 10))
	}
	return fingerprintFromParts(parts)
}

func (a *App) archiveContentFingerprint(ctx context.Context, backend backendpkg.Backend, rel string) string {
	archive, err := archivepkg.Open(ctx, backend, rel, a.cfg.Server.CacheDir)
	if err != nil {
		return ""
	}
	defer archive.Close()
	entries := []archivepkg.ArchiveEntry{}
	for _, entry := range archive.Entries() {
		if entry.IsDir || !shared.IsImageFile(entry.Name) {
			continue
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool { return shared.NaturalLess(entries[i].Name, entries[j].Name) })
	parts := []string{"archive", strconv.Itoa(len(entries))}
	for _, item := range sampleArchiveEntries(entries) {
		parts = append(parts, item.Name, strconv.FormatInt(item.Size, 10))
	}
	return fingerprintFromParts(parts)
}

func seriesContentFingerprint(candidates []chapterCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	ordered := append([]chapterCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return shared.NaturalLess(ordered[i].SortKey, ordered[j].SortKey) })
	parts := []string{"series", strconv.Itoa(len(ordered))}
	for _, item := range ordered {
		parts = append(parts, normalizeMetaKey(item.ChapterTitle), strconv.Itoa(item.PageCount), item.SourceType)
	}
	return fingerprintFromParts(parts)
}

func sampleEntries(items []backendpkg.Entry) []backendpkg.Entry {
	if len(items) <= 6 {
		return items
	}
	out := append([]backendpkg.Entry(nil), items[:3]...)
	out = append(out, items[len(items)-3:]...)
	return out
}

func sampleArchiveEntries(items []archivepkg.ArchiveEntry) []archivepkg.ArchiveEntry {
	if len(items) <= 6 {
		return items
	}
	out := append([]archivepkg.ArchiveEntry(nil), items[:3]...)
	out = append(out, items[len(items)-3:]...)
	return out
}

func metadataInputForDir(lib configpkg.LibraryConfig, rel string, title string, images []backendpkg.Entry) metadatapkg.ScanInput {
	return metadatapkg.ScanInput{
		Locator:            metadatapkg.Locator{LibraryID: lib.ID, RootType: "dir", RootRef: shared.CleanRel(rel)},
		FolderPath:         metadataFolderPath(lib, rel),
		ContentFingerprint: dirContentFingerprint(images),
		Hint:               metadataHintForRoot(rel, title),
	}
}

func metadataInputForArchive(ctx context.Context, a *App, lib configpkg.LibraryConfig, backend backendpkg.Backend, rel string, title string) metadatapkg.ScanInput {
	return metadatapkg.ScanInput{
		Locator:            metadatapkg.Locator{LibraryID: lib.ID, RootType: "archive", RootRef: shared.CleanRel(rel)},
		FolderPath:         metadataFolderPath(lib, rel),
		ContentFingerprint: a.archiveContentFingerprint(ctx, backend, rel),
		Hint:               metadataHintForRoot(rel, title),
	}
}

func metadataInputForSeries(lib configpkg.LibraryConfig, rel string, title string, candidates []chapterCandidate) metadatapkg.ScanInput {
	return metadatapkg.ScanInput{
		Locator:            metadatapkg.Locator{LibraryID: lib.ID, RootType: "series", RootRef: shared.CleanRel(rel)},
		FolderPath:         metadataFolderPath(lib, rel),
		ContentFingerprint: seriesContentFingerprint(candidates),
		Hint:               metadataHintForRoot(rel, title),
	}
}
