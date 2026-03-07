package main

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWarmPageMemoryFromDiskCacheLoadsPage(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "page.jpg")
	want := bytes.Repeat([]byte("a"), 1024)
	if err := os.WriteFile(cachePath, want, 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	s := &apiServer{pageMemoryCache: newPageMemoryCache(1 << 20), log: log.New(io.Discard, "", 0)}
	info := resolvedPageCacheInfo{key: "page-key", path: cachePath, contentType: "image/jpeg", modTime: time.Now()}
	page := PageRef{Name: "page.jpg", SourceRef: "page.jpg"}

	warmed, err := s.warmPageMemoryFromDiskCache(info, page)
	if err != nil {
		t.Fatalf("warmPageMemoryFromDiskCache: %v", err)
	}
	if !warmed {
		t.Fatal("expected disk cache page to be warmed into memory")
	}
	entry, ok := s.pageMemoryCache.Get(info.key)
	if !ok {
		t.Fatal("expected page to be present in memory cache")
	}
	if !bytes.Equal(entry.data, want) {
		t.Fatalf("unexpected memory cache bytes: got %d want %d", len(entry.data), len(want))
	}
}

func TestServePageFromDiskCacheSchedulesMemoryWarm(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "page.jpg")
	want := bytes.Repeat([]byte("b"), 2048)
	if err := os.WriteFile(cachePath, want, 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	s := &apiServer{pageMemoryCache: newPageMemoryCache(1 << 20), log: log.New(io.Discard, "", 0)}
	info := resolvedPageCacheInfo{key: "disk-page", path: cachePath, contentType: "image/jpeg", modTime: time.Now()}
	page := PageRef{Name: "page.jpg", SourceRef: "page.jpg"}
	rec := httptest.NewRecorder()

	if !s.servePageFromDiskCache(rec, info, page) {
		t.Fatal("expected disk cache serve to succeed")
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("unexpected served bytes: got %d want %d", len(got), len(want))
	}

	deadline := time.Now().Add(time.Second)
	for {
		entry, ok := s.pageMemoryCache.Get(info.key)
		if ok {
			if !bytes.Equal(entry.data, want) {
				t.Fatalf("unexpected warmed bytes: got %d want %d", len(entry.data), len(want))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected disk hit to warm page into memory asynchronously")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
