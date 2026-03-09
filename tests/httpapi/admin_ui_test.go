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
	for _, needle := range []string{
		"Metadata Admin",
		"themeToggleBtn",
		"rescanLibraryBtn",
		"cleanupDryRunBtn",
		"sidecarModal",
		"/api/v1/admin/metadata/sidecar",
		"EH Bot Pull / Import",
		"botTabBtn",
		"metadataTabBtn",
		"jobsTabBtn",
		"runEHBotPullBtn",
		"createEHBotJobBtn",
		"saveEHBotConfigBtn",
		"jobHistoryTable",
		"/api/v1/admin/ehbot/status",
		"/api/v1/admin/ehbot/config",
		"/api/v1/admin/ehbot/jobs/create",
		"/api/v1/admin/jobs",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("expected admin page to contain %q, got %q", needle, text)
		}
	}
	if strings.ContainsRune(text, '\uFFFD') {
		t.Fatalf("expected admin page without invalid utf-8 replacement chars, got %q", text)
	}
}
