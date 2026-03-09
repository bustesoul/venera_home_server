package httpapi_test

import (
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"testing"

	httpapipkg "venera_home_server/httpapi"
	"venera_home_server/tests/testkit"
)

func TestAdminJobsEndpointIncludesTrackedOperations(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Tracked Book", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	refresh := testkit.PostJSON(t, srv.URL+"/api/v1/admin/rescan", cfg.Server.Token, map[string]any{"library_id": "local-main"})
	refreshID := refresh["data"].(map[string]any)["job_id"].(string)
	job := waitForMetadataJob(t, srv.URL, cfg.Server.Token, refreshID)
	if job["status"] != "done" {
		t.Fatalf("expected rescan-backed refresh job done, got %#v", job)
	}

	_ = testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/cleanup", cfg.Server.Token, map[string]any{
		"library_id":      "local-main",
		"older_than_days": 0,
		"dry_run":         true,
	})

	history := testkit.GetJSON(t, srv.URL+"/api/v1/admin/jobs?limit=20", cfg.Server.Token)
	items := history["data"].(map[string]any)["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("expected at least two tracked job history items, got %#v", items)
	}
	seenKinds := map[string][]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		if kind, _ := item["kind"].(string); kind != "" {
			seenKinds[kind] = append(seenKinds[kind], item)
		}
	}
	refreshItems := seenKinds["metadata.refresh"]
	if len(refreshItems) == 0 {
		t.Fatalf("expected metadata.refresh in job history, got %#v", items)
	}
	var refreshItem map[string]any
	for _, item := range refreshItems {
		if item["trigger"] == "rescan" {
			refreshItem = item
			break
		}
	}
	if refreshItem == nil {
		t.Fatalf("expected rescan-triggered metadata.refresh in job history, got %#v", refreshItems)
	}
	if refreshItem["status"] != "done" {
		t.Fatalf("unexpected metadata.refresh history item: %#v", refreshItem)
	}
	cleanupItems := seenKinds["metadata.cleanup"]
	if len(cleanupItems) == 0 {
		t.Fatalf("expected metadata.cleanup in job history, got %#v", items)
	}
	cleanupItem := cleanupItems[0]
	if cleanupItem["trigger"] != "manual" || cleanupItem["status"] != "done" {
		t.Fatalf("unexpected metadata.cleanup history item: %#v", cleanupItem)
	}
}
