package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
	httpapipkg "venera_home_server/httpapi"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
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
	testkit.MustWriteZip(t, filepath.Join(root, "2021.6", "Archive Shelf", "Bundle.cbz"), map[string][]byte{
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

	pathSearch := testkit.GetJSON(t, srv.URL+"/api/v1/search?q=2021.6", cfg.Server.Token)
	pathItems := pathSearch["data"].(map[string]any)["items"].([]any)
	if len(pathItems) != 1 {
		t.Fatalf("expected 1 path search result, got %d", len(pathItems))
	}
	if pathItems[0].(map[string]any)["title"] != "Zipped Book" {
		t.Fatalf("expected path search to find zipped book, got %#v", pathItems[0])
	}

	pathModeSearch := testkit.GetJSON(t, srv.URL+"/api/v1/search?q=path:archive%20shelf", cfg.Server.Token)
	pathModeItems := pathModeSearch["data"].(map[string]any)["items"].([]any)
	if len(pathModeItems) != 1 {
		t.Fatalf("expected 1 path: search result, got %d", len(pathModeItems))
	}
	if pathModeItems[0].(map[string]any)["title"] != "Zipped Book" {
		t.Fatalf("expected path: search to find zipped book, got %#v", pathModeItems[0])
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

func TestCoverLazyThumbnailStoredInSeparateSQLite(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteSolidJPEG(t, filepath.Join(root, "Cover Book", "001.jpg"), 800, 400, color.RGBA{R: 220, G: 40, B: 40, A: 255})

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comic := findComicByTitle(t, application, "local-main", "Cover Book")
	locator := metadatapkg.Locator{LibraryID: comic.LibraryID, RootType: comic.RootType, RootRef: comic.RootRef}
	beforeThumb, err := application.MetadataStore().GetCoverThumbnail(context.Background(), locator)
	if err != nil {
		t.Fatalf("GetCoverThumbnail before request: %v", err)
	}
	if beforeThumb != nil {
		t.Fatalf("expected no cover thumbnail before first request, got %#v", beforeThumb)
	}
	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID, cfg.Server.Token)
	coverURL := details["data"].(map[string]any)["cover_url"].(string)

	firstRes, err := http.Get(coverURL)
	if err != nil {
		t.Fatalf("first cover request: %v", err)
	}
	defer firstRes.Body.Close()
	firstBody, _ := io.ReadAll(firstRes.Body)
	if got := firstRes.Header.Get("Content-Type"); !strings.HasPrefix(got, "image/jpeg") {
		t.Fatalf("expected jpeg cover thumbnail, got %q", got)
	}
	cfgJPEG, err := jpeg.DecodeConfig(bytes.NewReader(firstBody))
	if err != nil {
		t.Fatalf("decode first cover thumbnail: %v", err)
	}
	if cfgJPEG.Width != 256 || cfgJPEG.Height != 128 {
		t.Fatalf("unexpected first thumbnail size: %dx%d", cfgJPEG.Width, cfgJPEG.Height)
	}
	afterThumb, err := application.MetadataStore().GetCoverThumbnail(context.Background(), locator)
	if err != nil {
		t.Fatalf("GetCoverThumbnail after request: %v", err)
	}
	if afterThumb == nil || len(afterThumb.Data) == 0 {
		t.Fatalf("expected stored cover thumbnail after first request, got %#v", afterThumb)
	}
	if _, err := os.Stat(application.MetadataStore().ThumbnailPath()); err != nil {
		t.Fatalf("expected thumbnail sqlite file: %v", err)
	}

	testkit.MustWriteFile(t, filepath.Join(root, "Cover Book", "001.jpg"), []byte("broken-source"))

	secondRes, err := http.Get(coverURL)
	if err != nil {
		t.Fatalf("second cover request: %v", err)
	}
	defer secondRes.Body.Close()
	secondBody, _ := io.ReadAll(secondRes.Body)
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatalf("expected second cover request to reuse stored thumbnail")
	}
}

func TestCoverThumbnailRegeneratesAfterRescanFingerprintChange(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Regen Book", "001.jpg")
	initialTime := time.Now().Add(-2 * time.Hour)
	testkit.MustWriteSolidJPEG(t, target, 800, 400, color.RGBA{R: 220, G: 40, B: 40, A: 255})
	if err := os.Chtimes(target, initialTime, initialTime); err != nil {
		t.Fatalf("Chtimes initial cover: %v", err)
	}

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	comic := findComicByTitle(t, application, "local-main", "Regen Book")
	details := testkit.GetJSON(t, srv.URL+"/api/v1/comics/"+comic.ID, cfg.Server.Token)
	coverURL := details["data"].(map[string]any)["cover_url"].(string)

	firstRes, err := http.Get(coverURL)
	if err != nil {
		t.Fatalf("first cover request: %v", err)
	}
	defer firstRes.Body.Close()
	firstBody, _ := io.ReadAll(firstRes.Body)
	firstCfg, err := jpeg.DecodeConfig(bytes.NewReader(firstBody))
	if err != nil {
		t.Fatalf("decode first cover thumbnail: %v", err)
	}
	if firstCfg.Width != 256 || firstCfg.Height != 128 {
		t.Fatalf("unexpected first thumbnail size: %dx%d", firstCfg.Width, firstCfg.Height)
	}

	testkit.MustWriteSolidJPEG(t, target, 400, 800, color.RGBA{R: 40, G: 40, B: 220, A: 255})
	updatedTime := initialTime.Add(2 * time.Hour)
	if err := os.Chtimes(target, updatedTime, updatedTime); err != nil {
		t.Fatalf("Chtimes updated cover: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	secondRes, err := http.Get(coverURL)
	if err != nil {
		t.Fatalf("second cover request: %v", err)
	}
	defer secondRes.Body.Close()
	secondBody, _ := io.ReadAll(secondRes.Body)
	if bytes.Equal(firstBody, secondBody) {
		t.Fatalf("expected thumbnail bytes to change after rescan fingerprint update")
	}
	secondCfg, err := jpeg.DecodeConfig(bytes.NewReader(secondBody))
	if err != nil {
		t.Fatalf("decode second cover thumbnail: %v", err)
	}
	if secondCfg.Width != 128 || secondCfg.Height != 256 {
		t.Fatalf("unexpected second thumbnail size: %dx%d", secondCfg.Width, secondCfg.Height)
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

func TestMetadataAdminEndpoints(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	refresh := testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/refresh", cfg.Server.Token, map[string]any{"library_id": "local-main"})
	jobID := refresh["data"].(map[string]any)["job_id"].(string)
	job := waitForMetadataJob(t, srv.URL, cfg.Server.Token, jobID)
	if job["status"] != "done" {
		t.Fatalf("expected metadata job done, got %#v", job)
	}

	records := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?state=empty&library_id=local-main", cfg.Server.Token)
	items := records["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 metadata record, got %d", len(items))
	}
	item := items[0].(map[string]any)
	if item["state"] != "empty" {
		t.Fatalf("expected empty state, got %#v", item["state"])
	}
}

func TestMetadataCleanupEndpoint(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "Book A")
	testkit.MustWriteFile(t, filepath.Join(bookDir, "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	if err := os.RemoveAll(bookDir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	missing := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?state=missing&library_id=local-main", cfg.Server.Token)
	items := missing["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 missing metadata record, got %d", len(items))
	}

	dryRun := testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/cleanup", cfg.Server.Token, map[string]any{"library_id": "local-main", "older_than_days": 0, "dry_run": true})
	dryData := dryRun["data"].(map[string]any)
	if int(dryData["matched"].(float64)) != 1 || int(dryData["deleted"].(float64)) != 0 {
		t.Fatalf("unexpected dry-run cleanup result: %#v", dryData)
	}

	realRun := testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/cleanup", cfg.Server.Token, map[string]any{"library_id": "local-main", "older_than_days": 0, "dry_run": false})
	realData := realRun["data"].(map[string]any)
	if int(realData["matched"].(float64)) != 1 || int(realData["deleted"].(float64)) != 1 {
		t.Fatalf("unexpected cleanup result: %#v", realData)
	}

	missing = testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?state=missing&library_id=local-main", cfg.Server.Token)
	items = missing["data"].(map[string]any)["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("expected missing metadata to be cleaned, got %d", len(items))
	}
}

func waitForMetadataJob(t *testing.T, baseURL, token, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job := testkit.GetJSON(t, baseURL+"/api/v1/admin/metadata/jobs/"+jobID, token)
		data := job["data"].(map[string]any)
		status := data["status"].(string)
		if status == "done" || status == "failed" {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("metadata job %s did not finish in time: %#v", jobID, data)
		}
		time.Sleep(25 * time.Millisecond)
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
	t.Cleanup(func() { _ = application.Close() })
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

func TestMetadataExternalSourceEndpoints(t *testing.T) {
	root := t.TempDir()
	matchedRel := `[Circle] Match Book 11 [Chinese] [DL]`
	testkit.MustWriteFile(t, filepath.Join(root, matchedRel, `001.jpg`), []byte("img"))
	testkit.MustSeedExDBGallery(t, filepath.Join(root, `data`, `externaldb`, `catalog.sqlite`), []testkit.ExDBGalleryRow{{
		GID:      `2708021`,
		Token:    `d774d5b991`,
		Title:    `[Circle] Match Book 11`,
		TitleJPN: `[Circle] Match Book 11 [Chinese] [DL]`,
		Artist:   `["artist one"]`,
		Category: `Doujinshi`,
		Rating:   4.72,
		Thumb:    `https://ehgt.org/example-cover.webp`,
	}})

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(body), "Metadata Admin") {
		t.Fatalf("expected admin html page, got %q", string(body))
	}

	sources := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/sources", cfg.Server.Token)
	sourceItems := sources["data"].(map[string]any)["items"].([]any)
	if len(sourceItems) != 1 {
		t.Fatalf("expected 1 metadata source, got %d", len(sourceItems))
	}
	sourceID := sourceItems[0].(map[string]any)["id"].(string)

	browse := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/sources/"+sourceID+"/records?q=Match%20Book", cfg.Server.Token)
	browseItems := browse["data"].(map[string]any)["browse"].(map[string]any)["items"].([]any)
	if len(browseItems) != 1 {
		t.Fatalf("expected 1 browsed source row, got %d", len(browseItems))
	}

	enrich := testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/enrich", cfg.Server.Token, map[string]any{"library_id": "local-main", "state": "empty", "limit": 20, "workers": 2})
	jobID := enrich["data"].(map[string]any)["job_id"].(string)
	job := waitForMetadataJob(t, srv.URL, cfg.Server.Token, jobID)
	if job["status"] != "done" {
		t.Fatalf("expected enrich job done, got %#v", job)
	}
	if int(job["updated"].(float64)) != 1 {
		t.Fatalf("expected 1 updated record, got %#v", job)
	}

	records := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?library_id=local-main&search=Match%20Book&page=1&limit=10", cfg.Server.Token)
	recordData := records["data"].(map[string]any)
	if int(recordData["total"].(float64)) != 1 {
		t.Fatalf("expected 1 searched record, got %#v", recordData)
	}
	recordItem := recordData["items"].([]any)[0].(map[string]any)
	if recordItem["source_id"] != "2708021" {
		t.Fatalf("expected source_id 2708021, got %#v", recordItem)
	}

	testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/records/actions", cfg.Server.Token, map[string]any{"action": "lock", "locator": recordItem["locator"]})

	locked := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?state=locked&library_id=local-main&search=Match%20Book&page=1&limit=10", cfg.Server.Token)
	lockedData := locked["data"].(map[string]any)
	if int(lockedData["total"].(float64)) != 1 {
		t.Fatalf("expected 1 locked record, got %#v", lockedData)
	}
	ready := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?state=ready&library_id=local-main&search=Match%20Book&page=1&limit=10", cfg.Server.Token)
	readyData := ready["data"].(map[string]any)
	if int(readyData["total"].(float64)) != 0 {
		t.Fatalf("expected locked record to be excluded from ready filter, got %#v", readyData)
	}

	testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/records/actions", cfg.Server.Token, map[string]any{"action": "unlock", "locator": recordItem["locator"]})
	testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/records/actions", cfg.Server.Token, map[string]any{"action": "reset", "locator": recordItem["locator"]})
	records = testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?library_id=local-main&search=Match%20Book&page=1&limit=10", cfg.Server.Token)
	recordItem = records["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if recordItem["source"] != nil || recordItem["source_id"] != nil {
		t.Fatalf("expected reset record to clear source fields, got %#v", recordItem)
	}
}
