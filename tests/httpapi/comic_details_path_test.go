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
