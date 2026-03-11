package shared

import "strings"

var languageCodeAliases = map[string]string{
	"chinese":             "zh",
	"simplified chinese":  "zh",
	"simplified-chinese":  "zh",
	"traditional chinese": "zh",
	"traditional-chinese": "zh",
	"zh":                  "zh",
	"zh-cn":               "zh",
	"zh-hans":             "zh",
	"zh-hant":             "zh",
	"english":             "en",
	"en":                  "en",
	"japanese":            "ja",
	"ja":                  "ja",
	"jp":                  "ja",
	"korean":              "ko",
	"ko":                  "ko",
	"russian":             "ru",
	"ru":                  "ru",
	"spanish":             "es",
	"es":                  "es",
	"french":              "fr",
	"fr":                  "fr",
	"portuguese":          "pt",
	"pt":                  "pt",
	"pt-br":               "pt",
	"vietnamese":          "vi",
	"vi":                  "vi",
	"thai":                "th",
	"th":                  "th",
	"german":              "de",
	"de":                  "de",
	"italian":             "it",
	"it":                  "it",
}

var languageTagValues = map[string]string{
	"zh": "chinese",
	"en": "english",
	"ja": "japanese",
	"ko": "korean",
	"ru": "russian",
	"es": "spanish",
	"fr": "french",
	"pt": "portuguese",
	"vi": "vietnamese",
	"th": "thai",
	"de": "german",
	"it": "italian",
}

func NormalizeLanguageCode(value string) string {
	normalized := normalizeLanguageValue(value)
	if normalized == "" {
		return ""
	}
	if mapped, ok := languageCodeAliases[normalized]; ok {
		return mapped
	}
	if head, _, ok := strings.Cut(normalized, "-"); ok {
		if mapped, exists := languageCodeAliases[head]; exists {
			return mapped
		}
		if len(head) == 2 || len(head) == 3 {
			return head
		}
	}
	if len(normalized) == 2 || len(normalized) == 3 {
		return normalized
	}
	return ""
}

func LanguageTagValue(value string) string {
	code := NormalizeLanguageCode(value)
	if code == "" {
		return ""
	}
	if tag, ok := languageTagValues[code]; ok {
		return tag
	}
	return code
}

func normalizeLanguageValue(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(normalized, "language:") {
		normalized = strings.TrimSpace(strings.TrimPrefix(normalized, "language:"))
	}
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return normalized
}
