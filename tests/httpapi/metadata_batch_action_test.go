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

func TestMetadataBatchLockActionAndLockedFilter(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("a"))
	testkit.MustWriteFile(t, filepath.Join(root, "Book B", "001.jpg"), []byte("b"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	records := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?library_id=local-main&page=1&limit=10", cfg.Server.Token)
	items := records["data"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 metadata records, got %d", len(items))
	}
	locators := make([]any, 0, len(items))
	for _, raw := range items {
		locators = append(locators, raw.(map[string]any)["locator"])
	}

	result := testkit.PostJSON(t, srv.URL+"/api/v1/admin/metadata/records/actions", cfg.Server.Token, map[string]any{
		"action":   "lock",
		"locators": locators,
	})
	data := result["data"].(map[string]any)
	if int(data["processed"].(float64)) != 2 {
		t.Fatalf("expected processed=2, got %#v", data)
	}

	locked := testkit.GetJSON(t, srv.URL+"/api/v1/admin/metadata/records?library_id=local-main&state=locked&page=1&limit=10", cfg.Server.Token)
	lockedItems := locked["data"].(map[string]any)["items"].([]any)
	if len(lockedItems) != 2 {
		t.Fatalf("expected 2 locked records, got %d", len(lockedItems))
	}
}
