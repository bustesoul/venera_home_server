package main

import (
    "context"
    "path/filepath"
    "testing"
)

func TestSidecarOverridesMetadata(t *testing.T) {
    root := t.TempDir()
    mustWriteFile(t, filepath.Join(root, "Comic A", "001.jpg"), []byte("img"))
    mustWriteFile(t, filepath.Join(root, "Comic A", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Original Title</Title><Writer>Original Author</Writer><Genre>Drama</Genre></ComicInfo>`))
    mustWriteFile(t, filepath.Join(root, "Comic A", ".venera.json"), []byte(`{"title":"Override Title","subtitle":"Override Subtitle","authors":["Override Author"],"tags":["Slice of Life","School"],"language":"zh"}`))

    cfg := &Config{
        Server: ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
        Scan: ScanConfig{Concurrency: 1, ExtractArchives: true},
        Metadata: MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
        Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
    }
    app, err := NewApp(cfg)
    if err != nil {
        t.Fatalf("NewApp: %v", err)
    }
    if err := app.Rescan(context.Background(), ""); err != nil {
        t.Fatalf("Rescan: %v", err)
    }
    if len(app.libraries["local-main"]) != 1 {
        t.Fatalf("expected 1 comic, got %d", len(app.libraries["local-main"]))
    }
    comic := app.comics[app.libraries["local-main"][0]]
    if comic.Title != "Override Title" {
        t.Fatalf("unexpected title: %s", comic.Title)
    }
    if comic.Subtitle != "Override Subtitle" {
        t.Fatalf("unexpected subtitle: %s", comic.Subtitle)
    }
    if len(comic.Authors) != 1 || comic.Authors[0] != "Override Author" {
        t.Fatalf("unexpected authors: %#v", comic.Authors)
    }
    if len(comic.Tags) != 2 || comic.Language != "zh" {
        t.Fatalf("unexpected tags/language: %#v %s", comic.Tags, comic.Language)
    }
}
