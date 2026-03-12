package httpapi_test

import (
	"bytes"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	httpapipkg "venera_home_server/httpapi"
)

func TestWarmPageMemoryFromDiskCacheLoadsPage(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "page.jpg")
	want := bytes.Repeat([]byte("a"), 1024)
	if err := os.WriteFile(cachePath, want, 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	s := httpapipkg.NewForTests(1<<20, log.New(io.Discard, "", 0))
	info := httpapipkg.ResolvedPageCacheInfo{Key: "page-key", Path: cachePath, ContentType: "image/jpeg", ModTime: time.Now()}
	page := apppkg.PageRef{Name: "page.jpg", SourceRef: "page.jpg"}

	warmed, err := s.WarmPageMemoryFromDiskCache(info, page)
	if err != nil {
		t.Fatalf("WarmPageMemoryFromDiskCache: %v", err)
	}
	if !warmed {
		t.Fatal("expected disk cache page to be warmed into memory")
	}
	entry, ok := s.PageMemoryCache.Get(info.Key)
	if !ok {
		t.Fatal("expected page to be present in memory cache")
	}
	if !bytes.Equal(entry.Data, want) {
		t.Fatalf("unexpected memory cache bytes: got %d want %d", len(entry.Data), len(want))
	}
}

func TestServePageFromDiskCacheSchedulesMemoryWarm(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "page.jpg")
	want := bytes.Repeat([]byte("b"), 2048)
	if err := os.WriteFile(cachePath, want, 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	s := httpapipkg.NewForTests(1<<20, log.New(io.Discard, "", 0))
	info := httpapipkg.ResolvedPageCacheInfo{Key: "disk-page", Path: cachePath, ContentType: "image/jpeg", ModTime: time.Now()}
	page := apppkg.PageRef{Name: "page.jpg", SourceRef: "page.jpg"}
	rec := httptest.NewRecorder()

	if !s.ServePageFromDiskCache(rec, info, page) {
		t.Fatal("expected disk cache serve to succeed")
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("unexpected served bytes: got %d want %d", len(got), len(want))
	}

	deadline := time.Now().Add(time.Second)
	for {
		entry, ok := s.PageMemoryCache.Get(info.Key)
		if ok {
			if !bytes.Equal(entry.Data, want) {
				t.Fatalf("unexpected warmed bytes: got %d want %d", len(entry.Data), len(want))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected disk hit to warm page into memory asynchronously")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServePageFromDiskCacheRefreshesFileModTime(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "page.jpg")
	if err := os.WriteFile(cachePath, []byte("cache"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	s := httpapipkg.NewForTests(1<<20, log.New(io.Discard, "", 0))
	info := httpapipkg.ResolvedPageCacheInfo{Key: "disk-touch", Path: cachePath, ContentType: "image/jpeg", ModTime: time.Now()}
	page := apppkg.PageRef{Name: "page.jpg", SourceRef: "page.jpg"}
	rec := httptest.NewRecorder()

	if !s.ServePageFromDiskCache(rec, info, page) {
		t.Fatal("expected disk cache serve to succeed")
	}
	stat, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !stat.ModTime().After(oldTime) {
		t.Fatalf("expected cache mtime to be refreshed, old=%v new=%v", oldTime, stat.ModTime())
	}
}
