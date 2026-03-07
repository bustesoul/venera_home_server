package archive_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	apppkg "venera_home_server/app"
	archivepkg "venera_home_server/archive"
	backendpkg "venera_home_server/backend"
	configpkg "venera_home_server/config"
	httpapipkg "venera_home_server/httpapi"
	"venera_home_server/shared"
	"venera_home_server/tests/testkit"
)

func TestArchiveReadersOpenRarAnd7z(t *testing.T) {
	root := t.TempDir()
	testkit.MustCopyFixture(t, "test.rar", filepath.Join(root, "Sample.cbr"))
	testkit.MustCopyFixture(t, "copy.7z", filepath.Join(root, "Sample.cb7"))

	storage := backendpkg.NewLocalBackend(root)
	cacheDir := filepath.Join(root, "cache")
	if err := shared.EnsureDir(cacheDir); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rel    string
		format string
	}{
		{rel: "Sample.cbr", format: "rar"},
		{rel: "Sample.cb7", format: "7z"},
	}
	for _, tc := range tests {
		arc, err := archivepkg.Open(context.Background(), storage, tc.rel, cacheDir)
		if err != nil {
			t.Fatalf("Open(%s): %v", tc.rel, err)
		}
		if arc.Format() != tc.format {
			t.Fatalf("archive %s format = %s, want %s", tc.rel, arc.Format(), tc.format)
		}
		entries := arc.Entries()
		if len(entries) == 0 {
			t.Fatalf("archive %s returned no entries", tc.rel)
		}
		var target *archivepkg.ArchiveEntry
		for i := range entries {
			if !entries[i].IsDir {
				target = &entries[i]
				break
			}
		}
		if target == nil {
			t.Fatalf("archive %s returned no file entries", tc.rel)
		}
		rc, err := arc.Open(context.Background(), target.Name)
		if err != nil {
			t.Fatalf("archive.Open(%s, %s): %v", tc.rel, target.Name, err)
		}
		buf := make([]byte, 32)
		n, err := rc.Read(buf)
		_ = rc.Close()
		_ = arc.Close()
		if err != nil && err != io.EOF {
			t.Fatalf("archive.Read(%s): %v", tc.rel, err)
		}
		if n == 0 {
			t.Fatalf("archive %s entry %s returned no data", tc.rel, target.Name)
		}
	}
}

func TestPDFComicFlow(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("pdf rendering currently requires Windows runtime")
	}

	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	testkit.MustWriteMinimalPDF(t, filepath.Join(libraryRoot, "PDF Book.pdf"), "Hello PDF")

	cfg := &configpkg.Config{
		Server:    configpkg.ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: libraryRoot, ScanMode: "auto"}},
	}
	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 pdf comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic.Title != "PDF Book" {
		t.Fatalf("unexpected comic title: %s", comic.Title)
	}
	chapter := comic.Chapters[0]
	pages, err := application.MaterializeChapterPages(context.Background(), chapter)
	if err != nil {
		t.Fatalf("MaterializeChapterPages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 pdf page, got %d", len(pages))
	}

	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	pagesResp := testkit.GetJSON(t, fmt.Sprintf("%s/api/v1/comics/%s/chapters/%s/pages", srv.URL, comic.ID, chapter.ID), cfg.Server.Token)
	images := pagesResp["data"].(map[string]any)["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("expected 1 page image, got %d", len(images))
	}

	req, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("media request: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8))
	if !bytes.Equal(raw, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatalf("expected PNG header, got %v", raw)
	}
}
