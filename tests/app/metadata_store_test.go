package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	apppkg "venera_home_server/app"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
)

func TestMetadataStoreFillsOnlyMissingFields(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Local Title</Title><Writer>Local Author</Writer></ComicInfo>`))

	application := newTestApp(t, root)
	comic := findAppComicByTitle(t, application, "local-main", "Local Title")
	locator := metadatapkg.Locator{LibraryID: comic.LibraryID, RootType: comic.RootType, RootRef: comic.RootRef}
	if err := application.UpdateMetadata(context.Background(), locator, metadatapkg.Update{
		Title:       "Remote Title",
		Subtitle:    "Remote Subtitle",
		Description: "Remote Description",
		Artists:     []string{"Remote Author"},
		Tags:        []string{"tag-a", "tag-b"},
		Language:    "ja",
		SourceURL:   "https://example.com/gallery/1",
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	updated := application.ComicByID(comic.ID)
	if updated == nil {
		t.Fatal("expected comic after rescan")
	}
	if updated.Title != "Local Title" {
		t.Fatalf("expected local title to win, got %q", updated.Title)
	}
	if updated.Subtitle != "Remote Subtitle" {
		t.Fatalf("expected remote subtitle fill, got %q", updated.Subtitle)
	}
	if updated.Description != "Remote Description" {
		t.Fatalf("expected remote description fill, got %q", updated.Description)
	}
	if len(updated.Authors) != 1 || updated.Authors[0] != "Local Author" {
		t.Fatalf("expected local authors to stay intact, got %#v", updated.Authors)
	}
	if len(updated.Tags) != 2 || updated.Tags[0] != "tag-a" || updated.Tags[1] != "tag-b" {
		t.Fatalf("expected remote tags fill, got %#v", updated.Tags)
	}
	if updated.Language != "ja" {
		t.Fatalf("expected remote language fill, got %q", updated.Language)
	}
	if updated.SourceURL != "https://example.com/gallery/1" {
		t.Fatalf("expected remote source url fill, got %q", updated.SourceURL)
	}
}

func TestMetadataStoreRebindsMovedComicByFingerprint(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "Shelf", "Book A")
	newDir := filepath.Join(root, "Shelf", "Book B")
	testkit.MustWriteFile(t, filepath.Join(oldDir, "001.jpg"), []byte("img-1"))
	testkit.MustWriteFile(t, filepath.Join(oldDir, "002.jpg"), []byte("img-2"))

	application := newTestApp(t, root)
	comic := findAppComicByTitle(t, application, "local-main", "Book A")
	locator := metadatapkg.Locator{LibraryID: comic.LibraryID, RootType: comic.RootType, RootRef: comic.RootRef}
	if err := application.UpdateMetadata(context.Background(), locator, metadatapkg.Update{
		Title:       "Persisted Title",
		Description: "Persisted Description",
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	retitled := findAppComicByTitle(t, application, "local-main", "Persisted Title")
	expectedRootRef := filepath.ToSlash(filepath.Join("Shelf", "Book B"))
	if retitled.RootRef != expectedRootRef {
		t.Fatalf("expected rebound root_ref %q, got %q", expectedRootRef, retitled.RootRef)
	}
	if retitled.Description != "Persisted Description" {
		t.Fatalf("expected rebound description, got %q", retitled.Description)
	}
	records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: "local-main", Limit: 10})
	if err != nil {
		t.Fatalf("MetadataRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 metadata record after rebind, got %d", len(records))
	}
	if records[0].RootRef != expectedRootRef {
		t.Fatalf("expected rebound record root_ref %q, got %q", expectedRootRef, records[0].RootRef)
	}
	if records[0].MissingSince != nil {
		t.Fatalf("expected rebound record to be active, got missing_since=%v", records[0].MissingSince)
	}
}

func findAppComicByTitle(t *testing.T, application *apppkg.App, libraryID, title string) *apppkg.Comic {
	t.Helper()
	for _, id := range application.LibraryComicIDs(libraryID) {
		comic := application.ComicByID(id)
		if comic != nil && comic.Title == title {
			return comic
		}
	}
	t.Fatalf("failed to find comic %s", title)
	return nil
}
