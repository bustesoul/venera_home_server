package app

import (
	"bytes"
	"strings"

	"venera_home_server/shared"
)

var galleryInfoLanguageMap = map[string]string{
	"chinese":             "zh",
	"simplified chinese":  "zh",
	"traditional chinese": "zh",
	"english":             "en",
	"japanese":            "ja",
	"korean":              "ko",
	"russian":             "ru",
	"spanish":             "es",
	"french":              "fr",
	"portuguese":          "pt",
	"vietnamese":          "vi",
	"thai":                "th",
	"german":              "de",
	"italian":             "it",
}

func applyGalleryInfo(meta *ParsedMetadata, raw []byte) {
	text := string(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return
	}

	lines := strings.Split(text, "\n")

	// Bare-title detection: if the first non-empty line has no colon, treat it as the title.
	// A subsequent "Title: xxx" line will override this via hasExplicitTitle.
	for _, line := range lines {
		first := strings.TrimSpace(line)
		if first == "" {
			continue
		}
		if !strings.Contains(first, ":") {
			meta.Title = first
		}
		break
	}

	comments := make([]string, 0, len(lines))
	parsedTags := []string{}
	inComments := false
	inTagBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inComments {
			// Collect multi-line "> group: tag1, tag2" entries after a bare "Tags:" header.
			if inTagBlock {
				if strings.HasPrefix(trimmed, ">") {
					tagLine := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
					if _, val, ok := strings.Cut(tagLine, ":"); ok {
						tagLine = strings.TrimSpace(val)
					}
					parsedTags = append(parsedTags, parseGalleryInfoTags(tagLine)...)
					continue
				}
				inTagBlock = false
				if len(parsedTags) > 0 {
					meta.Tags = shared.UniqueStrings(append(meta.Tags, parsedTags...))
				}
			}
			if isGalleryInfoCommentsHeader(trimmed) {
				inComments = true
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "title":
				if value != "" {
					meta.Title = value
					meta.hasExplicitTitle = true
				}
			case "tags":
				parsedTags = parseGalleryInfoTags(value)
				if len(parsedTags) > 0 {
					meta.Tags = shared.UniqueStrings(append(meta.Tags, parsedTags...))
				} else {
					inTagBlock = true
				}
			}
			continue
		}
		if isGalleryInfoFooter(trimmed) {
			break
		}
		comments = append(comments, line)
	}

	// Flush any remaining tag block at end of file.
	if inTagBlock && len(parsedTags) > 0 {
		meta.Tags = shared.UniqueStrings(append(meta.Tags, parsedTags...))
	}

	description := joinGalleryInfoLines(comments)
	meta.Description = firstNonEmpty(description, meta.Description)
	meta.Language = firstNonEmpty(languageFromGalleryInfoTags(parsedTags), meta.Language)
}

func isGalleryInfoCommentsHeader(line string) bool {
	normalized := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(normalized, "uploader") && strings.Contains(normalized, "comment")
}

func isGalleryInfoFooter(line string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "downloaded from e-hentai galleries by the hentai@home downloader")
}

func parseGalleryInfoTags(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return shared.UniqueStrings(out)
}

func joinGalleryInfoLines(lines []string) string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func languageFromGalleryInfoTags(tags []string) string {
	for _, tag := range tags {
		if language := normalizeGalleryInfoLanguageTag(tag); language != "" {
			return language
		}
	}
	return ""
}

func normalizeGalleryInfoLanguageTag(tag string) string {
	normalized := strings.ToLower(strings.TrimSpace(tag))
	value := ""
	if strings.HasPrefix(normalized, "language:") {
		value = strings.TrimSpace(strings.TrimPrefix(normalized, "language:"))
	} else {
		value = normalized
	}
	if mapped, ok := galleryInfoLanguageMap[value]; ok {
		return mapped
	}
	if len(value) == 2 || len(value) == 3 {
		return value
	}
	return ""
}
