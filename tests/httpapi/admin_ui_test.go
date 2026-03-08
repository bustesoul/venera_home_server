package httpapi_test

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	httpapipkg "venera_home_server/httpapi"
	"venera_home_server/tests/testkit"
)

func TestAdminHomePageUsesReadableUTF8Text(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Demo Book", "001.jpg"), []byte("img"))

	cfg := newServerTestConfig(root, 16)
	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()

	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "text/html") || !strings.Contains(strings.ToLower(got), "charset=utf-8") {
		t.Fatalf("unexpected content type: %q", got)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "批量补全") || !strings.Contains(text, "数据源列表") || !strings.Contains(text, "保存 Token") {
		t.Fatalf("expected readable admin page text, got %q", text)
	}
	if strings.Contains(text, "閹") || strings.Contains(text, "鈧") {
		t.Fatalf("expected admin page without mojibake, got %q", text)
	}
}
