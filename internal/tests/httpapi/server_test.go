package httpapi_test

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	apppkg "venera_home_server/internal/app"
	configpkg "venera_home_server/internal/config"
	httpapipkg "venera_home_server/internal/httpapi"
	"venera_home_server/internal/tests/testkit"
)

func TestLocalServerFlow(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "001.jpg"), []byte("img1"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "002.jpg"), []byte("img2"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 01</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "001.jpg"), []byte("img3"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 02</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	testkit.MustWriteFile(t, filepath.Join(root, "Standalone", "001.jpg"), []byte("img4"))
	testkit.MustWriteFile(t, filepath.Join(root, "Standalone", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Standalone Book</Title><Writer>Jane Doe</Writer><Genre>Drama,Slice of Life</Genre></ComicInfo>`))
	testkit.MustWriteZip(t, filepath.Join(root, "Bundle.cbz"), map[string][]byte{
		"001.jpg":       []byte("zipimg"),
		"ComicInfo.xml": []byte(`<ComicInfo><Title>Zipped Book</Title><Writer>John Doe</Writer><Genre>Mystery</Genre></ComicInfo>`),
	})

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	bootstrap := testkit.GetJSON(t, srv.URL+"/api/v1/bootstrap", cfg.Server.Token)
	libs := bootstrap["data"].(map[string]any)["libraries"].([]any)
	if len(libs) != 1 {
		t.Fatalf("expected 1 library, got %d", len(libs))
	}

	comicsList := testkit.GetJSON(t, srv.URL+"/api/v1/comics?page=1&page_size=20", cfg.Server.Token)
	items := comicsList["data"].(map[string]any)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 comics, got %d", len(items))
	}

	search := testkit.GetJSON(t, srv.URL+"/api/v1/search?q=John%20Doe", cfg.Server.Token)
	searchItems := search["data"].(map[string]any)["items"].([]any)
	if len(searchItems) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(searchItems))
	}

	var zippedID string
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["title"] == "Zipped Book" {
			zippedID = item["id"].(string)
		}
	}
	if zippedID == "" {
		t.Fatal("failed to find zipped comic")
	}

	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+zippedID, cfg.Server.Token)
	data := details["data"].(map[string]any)
	chapters := data["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(chapters))
	}
	chapterID := chapters[0].(map[string]any)["id"].(string)

	pages := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+zippedID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
	images := pages["data"].(map[string]any)["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}

	req, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("media request: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if string(raw) != "zipimg" {
		t.Fatalf("unexpected media body: %q", string(raw))
	}
	if res.Header.Get("Cache-Control") == "" || res.Header.Get("ETag") == "" {
		t.Fatalf("expected media cache headers, got Cache-Control=%q ETag=%q", res.Header.Get("Cache-Control"), res.Header.Get("ETag"))
	}
	cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files: %v", err)
	}
	if len(cachedPages) == 0 {
		if cachedPages, err = testkit.WaitForRenderedCacheCount(root, 1, 3*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	condReq, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	condReq.Header.Set("If-None-Match", res.Header.Get("ETag"))
	condRes, err := http.DefaultClient.Do(condReq)
	if err != nil {
		t.Fatalf("conditional media request: %v", err)
	}
	defer condRes.Body.Close()
	if condRes.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304 for conditional request, got %d", condRes.StatusCode)
	}

	testkit.PostJSON(t, srv.URL+"/api/v1/favorites/folders", cfg.Server.Token, map[string]any{"name": "Reading"})
	testkit.PostJSON(t, srv.URL+"/api/v1/favorites/items", cfg.Server.Token, map[string]any{"comic_id": zippedID, "folder_id": "default"})
	folders := testkit.GetJSON(t, srv.URL+"/api/v1/favorites/folders?comic_id="+zippedID, cfg.Server.Token)
	favorited := folders["data"].(map[string]any)["favorited"].([]any)
	if len(favorited) == 0 {
		t.Fatal("expected favorite folder membership")
	}
}

func TestArchivePageRequestBuildsOnlyRequestedPageAsync(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteZip(t, filepath.Join(root, "Prefetch.cbz"), map[string][]byte{
		"001.jpg": []byte("page1"),
		"002.jpg": []byte("page2"),
		"003.jpg": []byte("page3"),
		"004.jpg": []byte("page4"),
	})

	cfg := newServerTestConfig(root, 0)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comic := findComicByTitle(t, application, "local-main", "Prefetch")
	chapterID := comic.Chapters[0].ID
	pages := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
	images := pages["data"].(map[string]any)["images"].([]any)
	if len(images) != 4 {
		t.Fatalf("expected 4 images, got %d", len(images))
	}
	time.Sleep(150 * time.Millisecond)
	cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files before media request: %v", err)
	}
	if len(cachedPages) != 0 {
		t.Fatalf("expected chapter page listing to avoid prefetch, got %d cached files", len(cachedPages))
	}

	req, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("media request: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.ReadAll(res.Body)

	if cachedPages, err = testkit.WaitForRenderedCacheCount(root, 1, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	cachedPages, err = filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files: %v", err)
	}
	if len(cachedPages) != 1 {
		t.Fatalf("expected only requested page to be cached, got %d files", len(cachedPages))
	}
}

func TestPageRequestsBuildOnlyRequestedPagesAsync(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 24; i++ {
		testkit.MustWriteFile(t, filepath.Join(root, "Windowed", fmt.Sprintf("%03d.jpg", i)), []byte(fmt.Sprintf("page-%02d", i)))
	}

	cfg := newServerTestConfig(root, 0)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comic := findComicByTitle(t, application, "local-main", "Windowed")
	chapterID := comic.Chapters[0].ID
	pages := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
	images := pages["data"].(map[string]any)["images"].([]any)
	if len(images) != 24 {
		t.Fatalf("expected 24 images, got %d", len(images))
	}

	firstReq, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	firstRes, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first media request: %v", err)
	}
	_, _ = io.ReadAll(firstRes.Body)
	_ = firstRes.Body.Close()

	cachedPages, err := testkit.WaitForRenderedCacheCount(root, 1, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	cachedPages, err = filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files after first request: %v", err)
	}
	if len(cachedPages) != 1 {
		t.Fatalf("expected first request to cache exactly 1 page, got %d", len(cachedPages))
	}

	secondReq, _ := http.NewRequest(http.MethodGet, images[10].(string), nil)
	secondRes, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second media request: %v", err)
	}
	_, _ = io.ReadAll(secondRes.Body)
	_ = secondRes.Body.Close()

	cachedPages, err = testkit.WaitForRenderedCacheCount(root, 2, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	cachedPages, err = filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files after second request: %v", err)
	}
	if len(cachedPages) != 2 {
		t.Fatalf("expected two requested pages to be cached, got %d", len(cachedPages))
	}
}

func TestFilePageServesFromMemoryAfterDiskCacheRemoval(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Direct", "001.jpg"), []byte("local-page"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comic := findComicByTitle(t, application, "local-main", "Direct")
	chapterID := comic.Chapters[0].ID
	pages := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
	images := pages["data"].(map[string]any)["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}

	firstReq, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	firstRes, err := http.DefaultClient.Do(firstReq)
	if err != nil {
		t.Fatalf("first media request: %v", err)
	}
	firstBody, _ := io.ReadAll(firstRes.Body)
	_ = firstRes.Body.Close()
	if string(firstBody) != "local-page" {
		t.Fatalf("unexpected first media body: %q", string(firstBody))
	}

	cachedPages, err := testkit.WaitForRenderedCacheCount(root, 1, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(cachedPages) != 1 {
		t.Fatalf("expected 1 rendered cache file, got %d", len(cachedPages))
	}
	if err := os.Remove(cachedPages[0]); err != nil {
		t.Fatalf("remove rendered cache file: %v", err)
	}

	secondReq, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	secondRes, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second media request: %v", err)
	}
	secondBody, _ := io.ReadAll(secondRes.Body)
	_ = secondRes.Body.Close()
	if string(secondBody) != "local-page" {
		t.Fatalf("unexpected second media body: %q", string(secondBody))
	}

	cachedPages, err = filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files after memory hit: %v", err)
	}
	if len(cachedPages) != 0 {
		t.Fatalf("expected memory hit without recreating disk cache, got %d files", len(cachedPages))
	}
}

func newServerTestConfig(root string, memoryCacheMB int) *configpkg.Config {
	return &configpkg.Config{
		Server:    configpkg.ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache"), MemoryCacheMB: memoryCacheMB},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
}

func newServerTestApp(t *testing.T, cfg *configpkg.Config) *apppkg.App {
	t.Helper()
	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return application
}

func findComicByTitle(t *testing.T, application *apppkg.App, libraryID, title string) *apppkg.Comic {
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
