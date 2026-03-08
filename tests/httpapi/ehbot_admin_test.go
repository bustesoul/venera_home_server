package httpapi_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	httpapipkg "venera_home_server/httpapi"
	"venera_home_server/tests/testkit"
)

func TestEHBotAdminEndpoints(t *testing.T) {
	artifactBytes, artifactSHA := buildAdminEHBotArtifact(t, map[string][]byte{
		"001.jpg":         []byte("img"),
		"galleryinfo.txt": []byte("Title:       Admin Imported Gallery\nTags:        language:english, other:test\n\nUploader's Comments:\n\nImported from admin API test"),
	})
	fake := newAdminFakeEHBotServer(artifactBytes, artifactSHA)
	botSrv := httptest.NewServer(fake)
	defer botSrv.Close()

	root := t.TempDir()
	comicsRoot := filepath.Join(root, "comics")
	if err := os.MkdirAll(comicsRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll comicsRoot: %v", err)
	}
	cfg := newServerTestConfig(comicsRoot, 16)
	cfg.Server.DataDir = filepath.Join(root, "data")
	cfg.Server.CacheDir = filepath.Join(root, "cache")
	cfg.EHBot.Enabled = false
	cfg.EHBot.BaseURL = botSrv.URL
	cfg.EHBot.ConsumerID = "home-admin"
	cfg.EHBot.TargetID = "home-admin"
	cfg.EHBot.TargetLibraryID = "local-main"
	cfg.EHBot.TargetSubdir = "EH Inbox"
	cfg.EHBot.LeaseSeconds = 120
	cfg.EHBot.DownloadTimeoutSeconds = 120
	cfg.EHBot.AutoRescan = true
	cfg.EHBot.MaxJobsPerPoll = 1

	application := newServerTestApp(t, cfg)
	srv := httptest.NewServer(httpapipkg.NewHTTPServer(application, log.New(io.Discard, "", 0)))
	defer srv.Close()

	status := testkit.GetJSON(t, srv.URL+"/api/v1/admin/ehbot/status", cfg.Server.Token)
	statusData := status["data"].(map[string]any)
	if configured, _ := statusData["configured"].(bool); !configured {
		t.Fatalf("expected configured ehbot status, got %#v", statusData)
	}
	if statusData["target_library_id"] != "local-main" {
		t.Fatalf("unexpected target library: %#v", statusData)
	}

	runOnce := testkit.PostJSON(t, srv.URL+"/api/v1/admin/ehbot/pull/run-once", cfg.Server.Token, map[string]any{})
	jobID := runOnce["data"].(map[string]any)["job_id"].(string)
	job := waitForEHBotAdminJob(t, srv.URL, cfg.Server.Token, jobID)
	if job["status"] != "done" {
		t.Fatalf("expected done job, got %#v", job)
	}
	if int(job["imported"].(float64)) != 1 {
		t.Fatalf("expected imported=1, got %#v", job)
	}

	jobs := testkit.GetJSON(t, srv.URL+"/api/v1/admin/ehbot/jobs", cfg.Server.Token)
	items := jobs["data"].(map[string]any)["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least one ehbot job")
	}

	comic := findComicByTitle(t, application, "local-main", "Admin Imported Gallery")
	if comic == nil {
		t.Fatal("expected imported comic from ehbot admin flow")
	}
}

func waitForEHBotAdminJob(t *testing.T, baseURL, token, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		jobs := testkit.GetJSON(t, baseURL+"/api/v1/admin/ehbot/jobs", token)
		items := jobs["data"].(map[string]any)["items"].([]any)
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["job_id"] == jobID {
				status := item["status"].(string)
				if status == "done" || status == "failed" {
					return item
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ehbot admin job %s did not finish in time", jobID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func buildAdminEHBotArtifact(t *testing.T, files map[string][]byte) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("Create zip entry: %v", err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatalf("Write zip entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close zip writer: %v", err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return buffer.Bytes(), hex.EncodeToString(sum[:])
}

type adminFakeEHBotServer struct {
	mu            sync.Mutex
	claimConsumer string
	artifact      []byte
	artifactSHA   string
}

func newAdminFakeEHBotServer(artifact []byte, artifactSHA string) *adminFakeEHBotServer {
	return &adminFakeEHBotServer{artifact: artifact, artifactSHA: artifactSHA}
}

func (f *adminFakeEHBotServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pull/jobs":
		f.writeJSON(w, map[string]any{"items": []map[string]any{f.jobPayload("ready")}, "count": 1})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/claim":
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		f.claimConsumer = stringValueAdmin(payload["consumer_id"])
		f.mu.Unlock()
		f.writeJSON(w, f.jobPayload("claimed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/heartbeat":
		f.writeJSON(w, f.jobPayload("claimed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/complete":
		f.writeJSON(w, f.jobPayload("completed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/fail":
		f.writeJSON(w, f.jobPayload("failed_retryable"))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pull/jobs/remote-job/artifact":
		f.mu.Lock()
		consumer := f.claimConsumer
		f.mu.Unlock()
		if strings.TrimSpace(r.URL.Query().Get("consumer_id")) != consumer {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"FORBIDDEN"}`)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("X-Artifact-SHA256", f.artifactSHA)
		_, _ = w.Write(f.artifact)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"NOT_FOUND"}`)
	}
}

func (f *adminFakeEHBotServer) jobPayload(status string) map[string]any {
	return map[string]any{
		"job_id":    "remote-job",
		"status":    status,
		"target_id": "home-admin",
		"gallery": map[string]any{
			"gid":        "3828219",
			"token":      "b71301f4cc",
			"title":      "Admin Imported Gallery",
			"source_url": "https://e-hentai.org/g/3828219/b71301f4cc/",
		},
		"artifact": map[string]any{
			"format":     "zip",
			"filename":   "[eh-3828219] Admin Imported Gallery.zip",
			"size_bytes": len(f.artifact),
			"sha256":     f.artifactSHA,
		},
	}
}

func (f *adminFakeEHBotServer) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

func stringValueAdmin(value any) string {
	if raw, ok := value.(string); ok {
		return raw
	}
	return ""
}
