package app_test

import (
	"path/filepath"
	"testing"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
	"venera_home_server/tests/testkit"
)

func TestAutoScanModeRequiresMatchingMetadataToMergeSiblingArchives(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteZip(t, filepath.Join(root, "25.08", "Book A.cbz"), map[string][]byte{"001.jpg": []byte("a")})
	testkit.MustWriteZip(t, filepath.Join(root, "25.08", "Book B.cbz"), map[string][]byte{"001.jpg": []byte("b")})

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 2 {
		t.Fatalf("expected 2 comics, got %d", len(ids))
	}
	titles := map[string]bool{}
	for _, id := range ids {
		titles[application.ComicByID(id).Title] = true
	}
	if !titles["Book A"] || !titles["Book B"] {
		t.Fatalf("unexpected titles: %#v", titles)
	}
}

func TestDirectorySidecarCanOverrideScanMode(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteZip(t, filepath.Join(root, "25.08", "Book A.cbz"), map[string][]byte{"001.jpg": []byte("a")})
	testkit.MustWriteZip(t, filepath.Join(root, "25.08", "Book B.cbz"), map[string][]byte{"001.jpg": []byte("b")})
	testkit.MustWriteFile(t, filepath.Join(root, "25.08", ".venera.json"), []byte(`{"scan_mode":"flat"}`))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "001.jpg"), []byte("img1"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 01</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "001.jpg"), []byte("img2"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 02</Title><Writer>Alpha Author</Writer></ComicInfo>`))

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 3 {
		t.Fatalf("expected 3 comics, got %d", len(ids))
	}

	var series *apppkg.Comic
	titles := map[string]bool{}
	for _, id := range ids {
		comic := application.ComicByID(id)
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
	testkit.MustWriteZip(t, filepath.Join(root, "Volume.cbz"), map[string][]byte{
		"01/001.jpg": []byte("a"),
		"01/002.jpg": []byte("b"),
		"02/001.jpg": []byte("c"),
	})

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if len(comic.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(comic.Chapters))
	}
	if comic.Chapters[0].PageCount != 2 || comic.Chapters[1].PageCount != 1 {
		t.Fatalf("unexpected chapter page counts: %d, %d", comic.Chapters[0].PageCount, comic.Chapters[1].PageCount)
	}
}

func newTestApp(t *testing.T, root string) *apppkg.App {
	t.Helper()
	cfg := &configpkg.Config{
		Server:    configpkg.ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	return application
}
