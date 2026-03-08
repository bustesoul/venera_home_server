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
	comments := make([]string, 0, len(lines))
	parsedTags := []string{}
	inComments := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inComments {
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
				}
			}
			continue
		}
		if isGalleryInfoFooter(trimmed) {
			break
		}
		comments = append(comments, line)
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
	if !strings.HasPrefix(normalized, "language:") {
		return ""
	}
	value := strings.TrimSpace(strings.TrimPrefix(normalized, "language:"))
	if mapped, ok := galleryInfoLanguageMap[value]; ok {
		return mapped
	}
	if len(value) == 2 || len(value) == 3 {
		return value
	}
	return ""
}
