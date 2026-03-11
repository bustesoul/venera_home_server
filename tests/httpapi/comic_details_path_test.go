package httpapi_test

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"testing"

	httpapipkg "venera_home_server/httpapi"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
)

func TestComicDetailsExposePathFields(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Path Book", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comics := testkit.GetJSON(t, srv.URL+"/api/v1/comics?page=1&page_size=20", cfg.Server.Token)
	items := comics["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(items))
	}
	comicID := items[0].(map[string]any)["id"].(string)

	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comicID, cfg.Server.Token)
	data := details["data"].(map[string]any)

	if got := data["relative_path"]; got != "Path Book" {
		t.Fatalf("unexpected relative_path: %#v", got)
	}
	wantLocalPath := filepath.Clean(filepath.Join(root, "Path Book"))
	if got := data["local_path"]; got != wantLocalPath {
		t.Fatalf("unexpected local_path: %#v", got)
	}

	tags := data["tags"].(map[string]any)
	if _, ok := tags["Path"]; ok {
		t.Fatalf("expected path to stay out of tags, got %#v", tags["Path"])
	}
	if _, ok := tags["RelativePath"]; ok {
		t.Fatalf("expected relative path to stay out of tags, got %#v", tags["RelativePath"])
	}
}

func TestComicDetailsGroupNamespacedTags(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Tagged Book", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Tagged Book", "galleryinfo.txt"), []byte(""+
		"Title: Tagged Book\n"+
		"Tags:\n"+
		"> artist: mizuryu kei\n"+
		"> female: dark skin, tomboy\n"+
		"> language: chinese\n"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comics := testkit.GetJSON(t, srv.URL+"/api/v1/comics?page=1&page_size=20", cfg.Server.Token)
	items := comics["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(items))
	}
	comicID := items[0].(map[string]any)["id"].(string)

	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comicID, cfg.Server.Token)
	data := details["data"].(map[string]any)
	tags := data["tags"].(map[string]any)

	assertTagGroup := func(group string, want ...string) {
		raw, ok := tags[group]
		if !ok {
			t.Fatalf("expected tag group %q in %#v", group, tags)
		}
		values := raw.([]any)
		if len(values) != len(want) {
			t.Fatalf("unexpected tag group %q size: got %#v want %#v", group, values, want)
		}
		for i, expected := range want {
			if values[i] != expected {
				t.Fatalf("unexpected %q[%d]: got %#v want %q", group, i, values[i], expected)
			}
		}
	}

	assertTagGroup("artist", "mizuryu kei")
	assertTagGroup("female", "dark skin", "tomboy")
	assertTagGroup("language", "chinese")
	if _, ok := tags["Tag"]; ok {
		t.Fatalf("expected all EH-style tags to be grouped, got raw Tag bucket %#v", tags["Tag"])
	}

	search := testkit.GetJSON(t, srv.URL+"/api/v1/search?q=tag:artist:mizuryu%20kei", cfg.Server.Token)
	searchItems := search["data"].(map[string]any)["items"].([]any)
	if len(searchItems) != 1 {
		t.Fatalf("expected 1 namespaced tag search result, got %d", len(searchItems))
	}
}

func TestComicDetailsInjectLanguageGroupFromLanguageField(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Language Book", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic == nil {
		t.Fatal("expected comic")
	}
	if err := application.UpdateMetadata(context.Background(), metadatapkg.Locator{
		LibraryID: comic.LibraryID,
		RootType:  comic.RootType,
		RootRef:   comic.RootRef,
	}, metadatapkg.Update{
		Language: "chinese",
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID, cfg.Server.Token)
	data := details["data"].(map[string]any)
	tags := data["tags"].(map[string]any)
	raw, ok := tags["language"]
	if !ok {
		t.Fatalf("expected language group in %#v", tags)
	}
	values := raw.([]any)
	if len(values) != 1 || values[0] != "chinese" {
		t.Fatalf("unexpected language group: %#v", values)
	}
}

func TestComicDetailsMergeAuthorsIntoArtistGroup(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Artist Book", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic == nil {
		t.Fatal("expected comic")
	}
	if err := application.UpdateMetadata(context.Background(), metadatapkg.Locator{
		LibraryID: comic.LibraryID,
		RootType:  comic.RootType,
		RootRef:   comic.RootRef,
	}, metadatapkg.Update{
		Artists: []string{"mizuryu kei"},
		Tags:    []string{"artist:mizuryu kei", "female:tomboy"},
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID, cfg.Server.Token)
	data := details["data"].(map[string]any)
	tags := data["tags"].(map[string]any)
	raw, ok := tags["artist"]
	if !ok {
		t.Fatalf("expected artist group in %#v", tags)
	}
	values := raw.([]any)
	if len(values) != 1 || values[0] != "mizuryu kei" {
		t.Fatalf("unexpected artist group: %#v", values)
	}
	if _, ok := tags["Author"]; ok {
		t.Fatalf("expected Author group to be omitted when artist tags exist, got %#v", tags["Author"])
	}
}
