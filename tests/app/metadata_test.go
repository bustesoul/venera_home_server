package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
)

func TestSidecarOverridesMetadata(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Comic A", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Comic A", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Original Title</Title><Writer>Original Author</Writer><Genre>Drama</Genre></ComicInfo>`))
	testkit.MustWriteFile(t, filepath.Join(root, "Comic A", ".venera.json"), []byte(`{"title":"Override Title","subtitle":"Override Subtitle","authors":["Override Author"],"tags":["Slice of Life","School"],"language":"zh"}`))

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
	if err := application.Rescan(context.Background(), ""); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
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

func TestHiddenSidecarSkipsDirectoryComic(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Hidden Book", "001.jpg"), []byte("img-hidden"))
	testkit.MustWriteFile(t, filepath.Join(root, "Hidden Book", ".venera.json"), []byte(`{"hidden":true}`))
	testkit.MustWriteFile(t, filepath.Join(root, "Visible Book", "001.jpg"), []byte("img-visible"))

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	if comic := application.ComicByID(ids[0]); comic == nil || comic.Title != "Visible Book" {
		t.Fatalf("unexpected visible comic: %#v", comic)
	}
}

func TestHiddenSidecarSkipsArchiveComic(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteZip(t, filepath.Join(root, "Hidden Book.cbz"), map[string][]byte{"001.jpg": []byte("a")})
	testkit.MustWriteFile(t, filepath.Join(root, "Hidden Book.cbz.venera.json"), []byte(`{"hidden":true}`))
	testkit.MustWriteZip(t, filepath.Join(root, "Visible Book.cbz"), map[string][]byte{"001.jpg": []byte("b")})

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	if comic := application.ComicByID(ids[0]); comic == nil || comic.Title != "Visible Book" {
		t.Fatalf("unexpected visible comic: %#v", comic)
	}
}

func TestArchiveEmbeddedGalleryInfoMetadata(t *testing.T) {
	root := t.TempDir()
	galleryInfo := `Title:       Example Gallery Title
Upload Time: 2026-03-08 12:44
Uploaded By: ExampleUploader
Downloaded:  2026-03-08 15:55
Tags:        language:chinese, parody:sample, female:yuri, other:ai generated

Uploader's Comments:

Original artist page: https://www.pixiv.net/users/102693044
Mirror note from uploader: https://www.pixiv.net/artworks/131144969

Story summary line.

Downloaded from E-Hentai Galleries by the Hentai@Home Downloader <3`
	testkit.MustWriteZip(t, filepath.Join(root, "Gallery.cbz"), map[string][]byte{
		"001.jpg":         []byte("img"),
		"galleryinfo.txt": []byte(galleryInfo),
	})

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic.Title != "Example Gallery Title" {
		t.Fatalf("unexpected title: %q", comic.Title)
	}
	if comic.Language != "zh" {
		t.Fatalf("unexpected language: %q", comic.Language)
	}
	if comic.SourceURL != "" {
		t.Fatalf("galleryinfo comments must not set source url, got %q", comic.SourceURL)
	}
	if !containsString(comic.Tags, "language:chinese") || !containsString(comic.Tags, "female:yuri") {
		t.Fatalf("unexpected tags: %#v", comic.Tags)
	}
	if !strings.Contains(comic.Description, "Story summary line.") {
		t.Fatalf("unexpected description: %q", comic.Description)
	}
	if strings.Contains(comic.Description, "Downloaded from E-Hentai Galleries") {
		t.Fatalf("footer should be trimmed from description: %q", comic.Description)
	}
	records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: "local-main", Limit: 10})
	if err != nil {
		t.Fatalf("MetadataRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 metadata record, got %d", len(records))
	}
	if records[0].Scanned.Title != "Example Gallery Title" {
		t.Fatalf("unexpected scanned title: %q", records[0].Scanned.Title)
	}
	if records[0].Scanned.Language != "zh" {
		t.Fatalf("unexpected scanned language: %q", records[0].Scanned.Language)
	}
	if !containsString(records[0].Scanned.Tags, "language:chinese") || !containsString(records[0].Scanned.Tags, "female:yuri") {
		t.Fatalf("unexpected scanned tags: %#v", records[0].Scanned.Tags)
	}
}

func TestDirectoryGalleryInfoMetadata(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Comic A", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Comic A", "galleryinfo.txt"), []byte(`Title: Directory Title
Tags: language:japanese, female:yuri, artist:mizuryu kei

Uploader's Comments:

Source: https://example.com/source/1

Directory description`))

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic.Title != "Directory Title" {
		t.Fatalf("unexpected title: %q", comic.Title)
	}
	if comic.Language != "ja" {
		t.Fatalf("unexpected language: %q", comic.Language)
	}
	if len(comic.Authors) != 1 || comic.Authors[0] != "mizuryu kei" {
		t.Fatalf("expected artist tag to populate authors, got %#v", comic.Authors)
	}
	if comic.SourceURL != "" {
		t.Fatalf("galleryinfo comments must not set source url, got %q", comic.SourceURL)
	}
	if !strings.Contains(comic.Description, "Directory description") {
		t.Fatalf("unexpected description: %q", comic.Description)
	}
	records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: "local-main", Limit: 10})
	if err != nil {
		t.Fatalf("MetadataRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 metadata record, got %d", len(records))
	}
	if records[0].Scanned.Title != "Directory Title" {
		t.Fatalf("unexpected scanned title: %q", records[0].Scanned.Title)
	}
	if records[0].Scanned.Language != "ja" {
		t.Fatalf("unexpected scanned language: %q", records[0].Scanned.Language)
	}
	if !containsString(records[0].Scanned.Tags, "language:japanese") || !containsString(records[0].Scanned.Tags, "female:yuri") {
		t.Fatalf("unexpected scanned tags: %#v", records[0].Scanned.Tags)
	}
	if len(records[0].Scanned.Artists) != 1 || records[0].Scanned.Artists[0] != "mizuryu kei" {
		t.Fatalf("expected scanned artists from artist tag, got %#v", records[0].Scanned.Artists)
	}
	if records[0].IsEmptyMetadata() {
		t.Fatalf("galleryinfo-backed record should not be empty: %#v", records[0])
	}
	readyRecords, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{
		LibraryID: "local-main",
		State:     "ready",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("MetadataRecords ready: %v", err)
	}
	if len(readyRecords) != 1 {
		t.Fatalf("expected 1 ready metadata record, got %d", len(readyRecords))
	}
	emptyRecords, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{
		LibraryID: "local-main",
		State:     "empty",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("MetadataRecords empty: %v", err)
	}
	if len(emptyRecords) != 0 {
		t.Fatalf("expected 0 empty metadata records, got %d", len(emptyRecords))
	}
}

// Tests the format produced by ehbot_server's buildInfoText (ehclient/client.go).
// First line is bare title, tags use multi-line "> group: values" syntax.
func TestEHBotDownloaderStyleGalleryInfo(t *testing.T) {
	root := t.TempDir()
	galleryInfo := "My EHBot Gallery\n" +
		"\u65E5\u672C\u8A9E\u30B5\u30D6\u30BF\u30A4\u30C8\u30EB\n" +
		"https://e-hentai.org/g/12345/abcdef/\n" +
		"\n" +
		"Category: Doujinshi\n" +
		"Uploader: testuser\n" +
		"Rating: 4.50\n" +
		"\n" +
		"Tags:\n" +
		"> language: chinese\n" +
		"> female: yuri, glasses\n" +
		"> male: test\n" +
		"\n" +
		"Uploader Comment:\n" +
		"Test comment from uploader\n" +
		"\n" +
		"Downloaded at Mon Jan 02 2006 15:04:05 GMT-0700 (MST)\n" +
		"\n" +
		"Generated by E-Hentai Downloader. https://github.com/ccloli/E-Hentai-Downloader"

	testkit.MustWriteZip(t, filepath.Join(root, "EHBot Gallery.cbz"), map[string][]byte{
		"001.jpg":         []byte("img"),
		"galleryinfo.txt": []byte(galleryInfo),
	})

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic.Title != "My EHBot Gallery" {
		t.Fatalf("unexpected title: %q", comic.Title)
	}
	if comic.Language != "zh" {
		t.Fatalf("unexpected language: %q", comic.Language)
	}
	if !containsString(comic.Tags, "language:chinese") || !containsString(comic.Tags, "female:yuri") || !containsString(comic.Tags, "female:glasses") || !containsString(comic.Tags, "male:test") {
		t.Fatalf("unexpected tags: %#v", comic.Tags)
	}
	if !strings.Contains(comic.Description, "Test comment from uploader") {
		t.Fatalf("unexpected description: %q", comic.Description)
	}
}

// Tests the format produced by ehbot_server's buildGalleryInfo (packager.go).
// Tags use the flat "> tags: namespace:tag1, namespace:tag2" syntax.
func TestEHBotPackagerStyleGalleryInfo(t *testing.T) {
	root := t.TempDir()
	galleryInfo := "Packager Gallery Title\n" +
		"\n" +
		"https://e-hentai.org/g/99999/fedcba/\n" +
		"\n" +
		"Category: \n" +
		"Uploader: someone\n" +
		"Posted: 2026-01-01\n" +
		"Rating: \n" +
		"\n" +
		"Tags:\n" +
		"> tags: language:chinese, female:yuri\n" +
		"\n" +
		"Downloaded at Mon Jan 02 2006 15:04:05 GMT-0700 (MST)\n" +
		"\n" +
		"Generated by E-Hentai Downloader. https://github.com/ccloli/E-Hentai-Downloader"

	testkit.MustWriteFile(t, filepath.Join(root, "Packager Comic", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Packager Comic", "galleryinfo.txt"), []byte(galleryInfo))

	application := newTestApp(t, root)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic.Title != "Packager Gallery Title" {
		t.Fatalf("unexpected title: %q", comic.Title)
	}
	if comic.Language != "zh" {
		t.Fatalf("unexpected language: %q", comic.Language)
	}
	if !containsString(comic.Tags, "language:chinese") || !containsString(comic.Tags, "female:yuri") {
		t.Fatalf("unexpected tags: %#v", comic.Tags)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
