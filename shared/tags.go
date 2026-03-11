package shared

import "strings"

func NormalizeTagNamespace(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func SplitNamespacedTag(value string) (string, string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.Contains(trimmed, ":") {
		return "", trimmed, false
	}
	namespace, tag, _ := strings.Cut(trimmed, ":")
	namespace = NormalizeTagNamespace(namespace)
	tag = strings.TrimSpace(tag)
	if namespace == "" || tag == "" {
		return "", trimmed, false
	}
	return namespace, tag, true
}

func NamespaceTag(namespace string, value string) string {
	trimmed := strings.Trim(strings.TrimSpace(value), `"'`)
	if trimmed == "" {
		return ""
	}
	namespace = NormalizeTagNamespace(namespace)
	if namespace == "" || namespace == "rest" {
		return trimmed
	}
	if _, _, ok := SplitNamespacedTag(trimmed); ok {
		return trimmed
	}
	return namespace + ":" + trimmed
}

func GroupTagsByNamespace(tags []string, genericKey string) map[string][]string {
	grouped := map[string][]string{}
	generic := make([]string, 0, len(tags))
	for _, raw := range tags {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		namespace, tag, ok := SplitNamespacedTag(raw)
		if ok {
			grouped[namespace] = append(grouped[namespace], tag)
			continue
		}
		generic = append(generic, raw)
	}
	for key, values := range grouped {
		grouped[key] = UniqueStrings(values)
	}
	if len(generic) > 0 {
		grouped[genericKey] = UniqueStrings(generic)
	}
	return grouped
}

func TagValuesByNamespace(tags []string, namespace string) []string {
	namespace = NormalizeTagNamespace(namespace)
	if namespace == "" {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tagNamespace, value, ok := SplitNamespacedTag(raw)
		if !ok || tagNamespace != namespace || strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	return UniqueStrings(out)
}
