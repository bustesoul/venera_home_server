package backend_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	apppkg "venera_home_server/internal/app"
	configpkg "venera_home_server/internal/config"
	"venera_home_server/internal/tests/testkit"
)

func TestWebDAVLibraryScan(t *testing.T) {
	zipBuffer := bytes.NewBuffer(nil)
	zw := zip.NewWriter(zipBuffer)
	w, err := zw.Create("001.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("zip-webdav"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipData := zipBuffer.Bytes()

	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/dav":
			testkit.WriteDAVResponse(w, server.URL, []testkit.DAVTestItem{
				{Href: "/dav/", IsDir: true},
				{Href: "/dav/Standalone/", IsDir: true},
				{Href: "/dav/Bundle.cbz", IsDir: false, Size: int64(len(zipData))},
			})
		case r.Method == "PROPFIND" && r.URL.Path == "/dav/Standalone":
			testkit.WriteDAVResponse(w, server.URL, []testkit.DAVTestItem{
				{Href: "/dav/Standalone/", IsDir: true},
				{Href: "/dav/Standalone/001.jpg", IsDir: false, Size: 4},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/dav/Standalone/001.jpg":
			_, _ = w.Write([]byte("imgw"))
		case r.Method == http.MethodGet && r.URL.Path == "/dav/Bundle.cbz":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipData)
		default:
			http.NotFound(w, r)
		}
	})
	server = httptest.NewServer(handler)
	defer server.Close()

	tempRoot := t.TempDir()
	cfg := &configpkg.Config{
		Server:    configpkg.ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(tempRoot, "data"), CacheDir: filepath.Join(tempRoot, "cache")},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: false, ReadSidecar: false},
		Libraries: []configpkg.LibraryConfig{{ID: "dav", Name: "WebDAV", Kind: "webdav", URL: server.URL, Path: "/dav", ScanMode: "auto"}},
	}

	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	ids := application.LibraryComicIDs("dav")
	if len(ids) != 2 {
		t.Fatalf("expected 2 comics from webdav, got %d", len(ids))
	}
	var foundZip bool
	for _, id := range ids {
		comic := application.ComicByID(id)
		if comic.Title == "Bundle" {
			foundZip = true
			chapter := comic.Chapters[0]
			pages, err := application.MaterializeChapterPages(context.Background(), chapter)
			if err != nil {
				t.Fatalf("MaterializeChapterPages: %v", err)
			}
			if len(pages) != 1 {
				t.Fatalf("expected 1 page, got %d", len(pages))
			}
		}
	}
	if !foundZip {
		t.Fatal("expected zipped comic from webdav")
	}
}
