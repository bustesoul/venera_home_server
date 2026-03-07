package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalServerFlow(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "001.jpg"), []byte("img1"))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "002.jpg"), []byte("img2"))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "01", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 01</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "001.jpg"), []byte("img3"))
	mustWriteFile(t, filepath.Join(root, "Series Alpha", "02", "ComicInfo.xml"), []byte(`<ComicInfo><Series>Series Alpha</Series><Title>Chapter 02</Title><Writer>Alpha Author</Writer></ComicInfo>`))
	mustWriteFile(t, filepath.Join(root, "Standalone", "001.jpg"), []byte("img4"))
	mustWriteFile(t, filepath.Join(root, "Standalone", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Standalone Book</Title><Writer>Jane Doe</Writer><Genre>Drama,Slice of Life</Genre></ComicInfo>`))
	mustWriteZip(t, filepath.Join(root, "Bundle.cbz"), map[string][]byte{
		"001.jpg":       []byte("zipimg"),
		"ComicInfo.xml": []byte(`<ComicInfo><Title>Zipped Book</Title><Writer>John Doe</Writer><Genre>Mystery</Genre></ComicInfo>`),
	})

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache"), MemoryCacheMB: 16},
		Scan:      ScanConfig{Concurrency: 2, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	srv := httptest.NewServer(newHTTPServer(app, log.New(io.Discard, "", 0)))
	defer srv.Close()

	bootstrap := getJSON(t, srv.URL+"/api/v1/bootstrap", cfg.Server.Token)
	libs := bootstrap["data"].(map[string]any)["libraries"].([]any)
	if len(libs) != 1 {
		t.Fatalf("expected 1 library, got %d", len(libs))
	}

	comicsList := getJSON(t, srv.URL+"/api/v1/comics?page=1&page_size=20", cfg.Server.Token)
	items := comicsList["data"].(map[string]any)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 comics, got %d", len(items))
	}

	search := getJSON(t, srv.URL+"/api/v1/search?q=John%20Doe", cfg.Server.Token)
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

	details := getJSON(t, srv.URL+"/api/v1/comics/"+zippedID, cfg.Server.Token)
	data := details["data"].(map[string]any)
	chapters := data["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter, got %d", len(chapters))
	}
	chapterID := chapters[0].(map[string]any)["id"].(string)

	pages := getJSON(t, srv.URL+"/api/v1/comics/"+zippedID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
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
		t.Fatal("expected rendered page cache file to be created")
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

	postJSON(t, srv.URL+"/api/v1/favorites/folders", cfg.Server.Token, map[string]any{"name": "Reading"})
	postJSON(t, srv.URL+"/api/v1/favorites/items", cfg.Server.Token, map[string]any{"comic_id": zippedID, "folder_id": "default"})
	folders := getJSON(t, srv.URL+"/api/v1/favorites/folders?comic_id="+zippedID, cfg.Server.Token)
	favorited := folders["data"].(map[string]any)["favorited"].([]any)
	if len(favorited) == 0 {
		t.Fatal("expected favorite folder membership")
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustWriteZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s failed: %s %s", url, res.Status, string(body))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func postJSON(t *testing.T, url, token string, payload map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("POST %s failed: %s %s", url, res.Status, string(body))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestArchivePageRequestTriggersChapterPrefetch(t *testing.T) {
	root := t.TempDir()
	mustWriteZip(t, filepath.Join(root, "Prefetch.cbz"), map[string][]byte{
		"001.jpg": []byte("page1"),
		"002.jpg": []byte("page2"),
		"003.jpg": []byte("page3"),
		"004.jpg": []byte("page4"),
	})

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	srv := httptest.NewServer(newHTTPServer(app, log.New(io.Discard, "", 0)))
	defer srv.Close()

	var comicID string
	for _, id := range app.libraries["local-main"] {
		if app.comics[id].Title == "Prefetch" {
			comicID = id
			break
		}
	}
	if comicID == "" {
		t.Fatal("failed to find prefetch comic")
	}
	chapterID := app.comics[comicID].Chapters[0].ID
	pages := getJSON(t, srv.URL+"/api/v1/comics/"+comicID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
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

	deadline := time.Now().Add(3 * time.Second)
	for {
		cachedPages, err = filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
		if err != nil {
			t.Fatalf("glob cache files: %v", err)
		}
		if len(cachedPages) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected chapter prefetch to cache additional pages, got %d files", len(cachedPages))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestPageRequestPrefetchUsesActualWindowStart(t *testing.T) {
	root := t.TempDir()
	for i := 1; i <= 24; i++ {
		mustWriteFile(t, filepath.Join(root, "Windowed", fmt.Sprintf("%03d.jpg", i)), []byte(fmt.Sprintf("page-%02d", i)))
	}

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	srv := httptest.NewServer(newHTTPServer(app, log.New(io.Discard, "", 0)))
	defer srv.Close()

	var comicID string
	for _, id := range app.libraries["local-main"] {
		if app.comics[id].Title == "Windowed" {
			comicID = id
			break
		}
	}
	if comicID == "" {
		t.Fatal("failed to find windowed comic")
	}
	chapterID := app.comics[comicID].Chapters[0].ID
	pages := getJSON(t, srv.URL+"/api/v1/comics/"+comicID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
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

	deadline := time.Now().Add(3 * time.Second)
	for {
		cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
		if err != nil {
			t.Fatalf("glob cache files after first prefetch: %v", err)
		}
		if len(cachedPages) >= 13 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected first window prefetch to warm 13 pages, got %d", len(cachedPages))
		}
		time.Sleep(25 * time.Millisecond)
	}

	secondReq, _ := http.NewRequest(http.MethodGet, images[10].(string), nil)
	secondRes, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatalf("second media request: %v", err)
	}
	_, _ = io.ReadAll(secondRes.Body)
	_ = secondRes.Body.Close()

	deadline = time.Now().Add(3 * time.Second)
	for {
		cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
		if err != nil {
			t.Fatalf("glob cache files after second prefetch: %v", err)
		}
		if len(cachedPages) >= 23 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected later window prefetch to warm additional pages, got %d", len(cachedPages))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestFilePageServesFromMemoryAfterDiskCacheRemoval(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Direct", "001.jpg"), []byte("local-page"))

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache"), MemoryCacheMB: 16},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	srv := httptest.NewServer(newHTTPServer(app, log.New(io.Discard, "", 0)))
	defer srv.Close()

	var comicID string
	for _, id := range app.libraries["local-main"] {
		if app.comics[id].Title == "Direct" {
			comicID = id
			break
		}
	}
	if comicID == "" {
		t.Fatal("failed to find direct comic")
	}
	chapterID := app.comics[comicID].Chapters[0].ID
	pages := getJSON(t, srv.URL+"/api/v1/comics/"+comicID+"/chapters/"+chapterID+"/pages", cfg.Server.Token)
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

	cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
	if err != nil {
		t.Fatalf("glob cache files: %v", err)
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
