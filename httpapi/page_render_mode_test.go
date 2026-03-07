package httpapi

import (
    "net/http/httptest"
    "testing"
)

func TestNormalizePageRenderMode(t *testing.T) {
    cases := map[string]PageRenderMode{
        "":         pageRenderModeDefault,
        "default":  pageRenderModeDefault,
        "origin":   pageRenderModeOrigin,
        "original": pageRenderModeOrigin,
        "ORIGIN":   pageRenderModeOrigin,
        "weird":    pageRenderModeDefault,
    }
    for input, want := range cases {
        if got := normalizePageRenderMode(input); got != want {
            t.Fatalf("normalizePageRenderMode(%q) = %q, want %q", input, got, want)
        }
    }
}

func TestPageRenderModeFromRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/media/demo", nil)
	if got := pageRenderModeFromRequest(req); got != pageRenderModeOrigin {
		t.Fatalf("default mode = %q, want %q", got, pageRenderModeOrigin)
	}

	req.Header.Set("X-Venera-Reader", "1")
	if got := pageRenderModeFromRequest(req); got != pageRenderModeDefault {
		t.Fatalf("reader mode = %q, want %q", got, pageRenderModeDefault)
	}

	req = httptest.NewRequest("GET", "/media/demo", nil)
	req.Header.Set("X-Venera-Image-Mode", "origin")
	if got := pageRenderModeFromRequest(req); got != pageRenderModeOrigin {
		t.Fatalf("header mode = %q, want %q", got, pageRenderModeOrigin)
	}

    req = httptest.NewRequest("GET", "/media/demo?mode=default", nil)
    req.Header.Set("X-Venera-Image-Mode", "origin")
    if got := pageRenderModeFromRequest(req); got != pageRenderModeDefault {
        t.Fatalf("query should win, got %q", got)
    }

    req = httptest.NewRequest("GET", "/media/demo?mode=origin", nil)
    if got := pageRenderModeFromRequest(req); got != pageRenderModeOrigin {
        t.Fatalf("query origin = %q, want %q", got, pageRenderModeOrigin)
    }
}

func TestMediaETagIncludesRenderMode(t *testing.T) {
	readerReq := httptest.NewRequest("GET", "/media/demo", nil)
	readerReq.Header.Set("X-Venera-Reader", "1")
	originReq := httptest.NewRequest("GET", "/media/demo?mode=origin", nil)

	if mediaETag(readerReq) == mediaETag(originReq) {
		t.Fatal("expected media etag to differ across render modes")
	}
}

func TestShouldVisualCompressSource(t *testing.T) {
    if !shouldVisualCompressSource("image/jpeg", ".jpg", largePageVisualCompressThreshold) {
        t.Fatal("expected large jpeg to be compressible")
    }
    if shouldVisualCompressSource("image/png", ".png", largePageVisualCompressThreshold) {
        t.Fatal("expected png to stay on original path")
    }
    if shouldVisualCompressSource("image/jpeg", ".jpg", largePageVisualCompressThreshold-1) {
        t.Fatal("expected jpeg below threshold to stay on original path")
    }
}

func TestShouldVisualCompressDimensions(t *testing.T) {
    if !shouldVisualCompressDimensions(4200, 3000) {
        t.Fatal("expected long edge above threshold to compress")
    }
    if shouldVisualCompressDimensions(4096, 3000) {
        t.Fatal("expected long edge at threshold to stay original")
    }
}
