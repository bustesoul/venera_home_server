package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
)

func TestCleanupCacheNowRemovesExpiredDiskCacheFiles(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache")
	cfg := &configpkg.Config{
		Server: configpkg.ServerConfig{
			Listen:                      "127.0.0.1:0",
			DataDir:                     filepath.Join(root, "data"),
			CacheDir:                    cacheDir,
			CacheMaxAgeHours:            0,
			CacheCleanupIntervalMinutes: 360,
		},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: false, ReadSidecar: false},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: filepath.Join(root, "comics"), ScanMode: "auto"}},
	}
	if err := os.MkdirAll(cfg.Libraries[0].Root, 0o755); err != nil {
		t.Fatalf("MkdirAll comics root: %v", err)
	}

	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	oldRendered := writeCacheFile(t, filepath.Join(cacheDir, "rendered-pages", "old.jpg"), []byte("old-rendered"), time.Now().Add(-3*time.Hour))
	newRendered := writeCacheFile(t, filepath.Join(cacheDir, "rendered-pages", "new.jpg"), []byte("new-rendered"), time.Now())
	oldArchive := writeCacheFile(t, filepath.Join(cacheDir, "archive-source", "old.cbz"), []byte("old-archive"), time.Now().Add(-3*time.Hour))
	newWebDAV := writeCacheFile(t, filepath.Join(cacheDir, "webdav", "dav-lib", "fresh.zip"), []byte("fresh-webdav"), time.Now())
	oldPDF := writeCacheFile(t, filepath.Join(cacheDir, "pdf", "deadbeef", "pages", "0001.png"), []byte("old-pdf"), time.Now().Add(-3*time.Hour))
	cfg.Server.CacheMaxAgeHours = 1

	result, err := application.CleanupCacheNow()
	if err != nil {
		t.Fatalf("CleanupCacheNow: %v", err)
	}
	if result.RemovedFiles < 3 {
		t.Fatalf("expected at least 3 removed files, got %#v", result)
	}

	assertMissing(t, oldRendered)
	assertMissing(t, oldArchive)
	assertMissing(t, oldPDF)
	assertPresent(t, newRendered)
	assertPresent(t, newWebDAV)
	if _, err := os.Stat(filepath.Join(cacheDir, "pdf", "deadbeef", "pages")); !os.IsNotExist(err) {
		t.Fatalf("expected empty expired pdf cache directory to be removed, stat err=%v", err)
	}
}

func writeCacheFile(t *testing.T, path string, data []byte, modTime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	return path
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err=%v", path, err)
	}
}

func assertPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}
