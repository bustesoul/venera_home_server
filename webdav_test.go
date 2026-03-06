package main

import (
    "archive/zip"
    "bytes"
    "context"
    "fmt"
    "io"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"
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
            writeDAVResponse(w, server.URL, []davTestItem{
                {Href: "/dav/", IsDir: true},
                {Href: "/dav/Standalone/", IsDir: true},
                {Href: "/dav/Bundle.cbz", IsDir: false, Size: int64(len(zipData))},
            })
        case r.Method == "PROPFIND" && r.URL.Path == "/dav/Standalone":
            writeDAVResponse(w, server.URL, []davTestItem{
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
    cfg := &Config{
        Server: ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(tempRoot, "data"), CacheDir: filepath.Join(tempRoot, "cache")},
        Scan: ScanConfig{Concurrency: 1, ExtractArchives: true},
        Metadata: MetadataConfig{ReadComicInfo: false, ReadSidecar: false},
        Libraries: []LibraryConfig{{ID: "dav", Name: "WebDAV", Kind: "webdav", URL: server.URL, Path: "/dav", ScanMode: "auto"}},
    }

    app, err := NewApp(cfg)
    if err != nil {
        t.Fatalf("NewApp: %v", err)
    }
    ids := app.libraries["dav"]
    if len(ids) != 2 {
        t.Fatalf("expected 2 comics from webdav, got %d", len(ids))
    }
    var foundZip bool
    for _, id := range ids {
        comic := app.comics[id]
        if comic.Title == "Bundle" {
            foundZip = true
            chapter := comic.Chapters[0]
            pages, err := app.materializeChapterPages(context.Background(), chapter)
            if err != nil {
                t.Fatalf("materializeChapterPages: %v", err)
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

type davTestItem struct {
    Href  string
    IsDir bool
    Size  int64
}

func writeDAVResponse(w http.ResponseWriter, base string, items []davTestItem) {
    w.Header().Set("Content-Type", "application/xml")
    w.WriteHeader(http.StatusMultiStatus)
    _, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><multistatus xmlns="DAV:">`)
    for _, item := range items {
        resource := ""
        if item.IsDir {
            resource = `<resourcetype><collection/></resourcetype>`
        } else {
            resource = `<resourcetype/>`
        }
        _, _ = io.WriteString(w, fmt.Sprintf(`<response><href>%s%s</href><propstat><prop>%s<getcontentlength>%d</getcontentlength><getlastmodified>Mon, 06 Mar 2026 12:00:00 GMT</getlastmodified></prop></propstat></response>`, base, item.Href, resource, item.Size))
    }
    _, _ = io.WriteString(w, `</multistatus>`)
}
