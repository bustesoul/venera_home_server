package main

import (
	"path/filepath"
	"testing"
)

func TestAutoScanModeRequiresMatchingMetadataToMergeSiblingArchives(t *testing.T) {
	root := t.TempDir()
	mustWriteZip(t, filepath.Join(root, "25.08", "Book A.cbz"), map[string][]byte{"001.jpg": []byte("a")})
	mustWriteZip(t, filepath.Join(root, "25.08", "Book B.cbz"), map[string][]byte{"001.jpg": []byte("b")})

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	ids := app.libraries["local-main"]
	if len(ids) != 2 {
		t.Fatalf("expected 2 comics, got %d", len(ids))
	}
	titles := map[string]bool{}
	for _, id := range ids {
		titles[app.comics[id].Title] = true
	}
	if !titles["Book A"] || !titles["Book B"] {
		t.Fatalf("unexpected titles: %#v", titles)
	}
}

func TestDirectorySidecarCanOverrideScanMode(t *testing.T) {
	root := t.TempDir()
	mustWriteZip(t, filepath.Join(root, "25.08", "Book A.cbz"), map[string][]byte{"001.jpg": []byte("a")})
	mustWriteZip(t, filepath.Join(root, "25.08", "Book B.cbz"), map[string][]byte{"001.jpg": []byte("b")})
	mustWriteFile(t, filepath.Join(root, "25.08", ".venera.json"), []byte(`{"scan_mode":"flat"}`))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "001.jpg"), []byte("img1"))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 01</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "001.jpg"), []byte("img2"))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 02</Title><Writer>Alpha Author</Writer></ComicInfo>`))

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	ids := app.libraries["local-main"]
	if len(ids) != 3 {
		t.Fatalf("expected 3 comics, got %d", len(ids))
	}

	var series *Comic
	titles := map[string]bool{}
	for _, id := range ids {
		comic := app.comics[id]
		titles[comic.Title] = true
		if comic.Title == "Series Alpha" {
			series = comic
		}
	}
	if !titles["Book A"] || !titles["Book B"] || !titles["Series Alpha"] {
		t.Fatalf("unexpected titles: %#v", titles)
	}
	if series == nil {
		t.Fatal("expected Series Alpha comic")
	}
	if len(series.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(series.Chapters))
	}
}

func TestSingleArchiveWithChapterFoldersBuildsMultiChapterComic(t *testing.T) {
	root := t.TempDir()
	mustWriteZip(t, filepath.Join(root, "Volume.cbz"), map[string][]byte{
		"01/001.jpg": []byte("a"),
		"01/002.jpg": []byte("b"),
		"02/001.jpg": []byte("c"),
	})

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	ids := app.libraries["local-main"]
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := app.comics[ids[0]]
	if len(comic.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(comic.Chapters))
	}
	if comic.Chapters[0].PageCount != 2 || comic.Chapters[1].PageCount != 1 {
		t.Fatalf("unexpected chapter page counts: %d, %d", comic.Chapters[0].PageCount, comic.Chapters[1].PageCount)
	}
}
