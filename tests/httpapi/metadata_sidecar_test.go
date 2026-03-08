package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	httpapipkg "venera_home_server/httpapi"
	"venera_home_server/tests/testkit"
)

func TestMetadataSidecarAPIForSeries(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Series Shelf", "01", "001.jpg"), []byte("img1"))
	testkit.MustWriteFile(t, filepath.Join(root, "Series Shelf", "02", "001.jpg"), []byte("img2"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	locator := map[string]any{
		"library_id": "local-main",
		"root_type":  "series",
		"root_ref":   "Series Shelf",
	}
	query := url.Values{
		"library_id": []string{"local-main"},
		"root_type":  []string{"series"},
		"root_ref":   []string{"Series Shelf"},
	}

	before := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/sidecar?"+query.Encode(), cfg.Server.Token)
	beforeData := before["data"].(map[string]any)
	if beforeData["writable"] != true {
		t.Fatalf("expected sidecar endpoint writable, got %#v", beforeData)
	}
	if beforeData["exists"] != false {
		t.Fatalf("expected sidecar to be missing initially, got %#v", beforeData)
	}

	putResp := requestJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/metadata/sidecar", cfg.Server.Token, map[string]any{
		"locator": locator,
		"content": `{"series":"Override Series","authors":["Tester"],"tags":["Tag A"],"scan_mode":"auto"}`,
	})
	putData := putResp["data"].(map[string]any)
	if putData["exists"] != true {
		t.Fatalf("expected sidecar to exist after save, got %#v", putData)
	}

	sidecarPath := filepath.Join(root, "Series Shelf", ".venera.json")
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar file: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"series": "Override Series"`)) {
		t.Fatalf("expected saved sidecar content, got %q", string(raw))
	}

	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("rescan after sidecar save: %v", err)
	}
	comic := findComicByTitle(t, application, "local-main", "Override Series")
	if comic.RootType != "series" {
		t.Fatalf("expected series comic, got %#v", comic)
	}
	if len(comic.Authors) != 1 || comic.Authors[0] != "Tester" {
		t.Fatalf("expected authors from sidecar, got %#v", comic.Authors)
	}
	if len(comic.Tags) != 1 || comic.Tags[0] != "Tag A" {
		t.Fatalf("expected tags from sidecar, got %#v", comic.Tags)
	}

	deleteResp := requestJSON(t, http.MethodDelete, srv.URL+"/api/v1/admin/metadata/sidecar", cfg.Server.Token, map[string]any{
		"locator": locator,
	})
	deleteData := deleteResp["data"].(map[string]any)
	if deleteData["exists"] != false {
		t.Fatalf("expected sidecar to be removed, got %#v", deleteData)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar file deleted, stat err=%v", err)
	}
}

func TestMetadataSidecarAPIForArchive(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteZip(t, filepath.Join(root, "Archive Shelf", "Bundle.cbz"), map[string][]byte{
		"001.jpg": []byte("img1"),
	})

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	locator := map[string]any{
		"library_id": "local-main",
		"root_type":  "archive",
		"root_ref":   "Archive Shelf/Bundle.cbz",
	}
	query := url.Values{
		"library_id": []string{"local-main"},
		"root_type":  []string{"archive"},
		"root_ref":   []string{"Archive Shelf/Bundle.cbz"},
	}

	before := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/sidecar?"+query.Encode(), cfg.Server.Token)
	beforeData := before["data"].(map[string]any)
	if beforeData["writable"] != true {
		t.Fatalf("expected archive sidecar endpoint writable, got %#v", beforeData)
	}
	if beforeData["exists"] != false {
		t.Fatalf("expected archive sidecar to be missing initially, got %#v", beforeData)
	}

	putResp := requestJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/metadata/sidecar", cfg.Server.Token, map[string]any{
		"locator": locator,
		"content": `{"title":"Override Archive","authors":["Zip Tester"],"tags":["Zip Tag"]}`,
	})
	putData := putResp["data"].(map[string]any)
	if putData["exists"] != true {
		t.Fatalf("expected archive sidecar to exist after save, got %#v", putData)
	}

	sidecarPath := filepath.Join(root, "Archive Shelf", "Bundle.cbz.venera.json")
	raw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read archive sidecar file: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"title": "Override Archive"`)) {
		t.Fatalf("expected saved archive sidecar content, got %q", string(raw))
	}

	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("rescan after archive sidecar save: %v", err)
	}
	comic := findComicByTitle(t, application, "local-main", "Override Archive")
	if comic.RootType != "archive" {
		t.Fatalf("expected archive comic, got %#v", comic)
	}
	if len(comic.Authors) != 1 || comic.Authors[0] != "Zip Tester" {
		t.Fatalf("expected authors from archive sidecar, got %#v", comic.Authors)
	}
	if len(comic.Tags) != 1 || comic.Tags[0] != "Zip Tag" {
		t.Fatalf("expected tags from archive sidecar, got %#v", comic.Tags)
	}

	deleteResp := requestJSON(t, http.MethodDelete, srv.URL+"/api/v1/admin/metadata/sidecar", cfg.Server.Token, map[string]any{
		"locator": locator,
	})
	deleteData := deleteResp["data"].(map[string]any)
	if deleteData["exists"] != false {
		t.Fatalf("expected archive sidecar to be removed, got %#v", deleteData)
	}
	if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
		t.Fatalf("expected archive sidecar file deleted, stat err=%v", err)
	}
}
func requestJSON(t *testing.T, method, rawURL, token string, payload map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("unexpected status %d: %s", res.StatusCode, string(raw))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}
