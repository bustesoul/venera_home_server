package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
)

func TestEHBotPullImportsArchiveAndRescans(t *testing.T) {
	artifactBytes, artifactSHA := buildEHBotArtifact(t, map[string][]byte{
		"001.jpg":         []byte("img"),
		"galleryinfo.txt": []byte("Title:       Example Gallery Title\nTags:        language:chinese, female:yuri\n\nUploader's Comments:\n\nImported by ehbot integration test"),
	})
	fake := newFakeEHBotServer(t, artifactBytes, artifactSHA)
	srv := httptest.NewServer(fake)
	defer srv.Close()

	root := t.TempDir()
	comicsRoot := filepath.Join(root, "comics")
	if err := os.MkdirAll(comicsRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll comicsRoot: %v", err)
	}
	cfg := &configpkg.Config{
		Server: configpkg.ServerConfig{
			Listen:   "127.0.0.1:0",
			DataDir:  filepath.Join(root, "data"),
			CacheDir: filepath.Join(root, "cache"),
		},
		Scan:     configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata: configpkg.MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		EHBot: configpkg.EHBotConfig{
			Enabled:                false,
			BaseURL:                srv.URL,
			ConsumerID:             "home-main",
			TargetID:               "home-main",
			TargetLibraryID:        "local-main",
			TargetSubdir:           "EH Inbox",
			LeaseSeconds:           120,
			DownloadTimeoutSeconds: 120,
			AutoRescan:             true,
			MaxJobsPerPoll:         1,
		},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: comicsRoot, ScanMode: "auto"}},
	}
	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer func() { _ = application.Close() }()

	job, err := application.StartEHBotPull(context.Background())
	if err != nil {
		t.Fatalf("StartEHBotPull: %v", err)
	}
	job = waitForEHBotJob(t, application, job.ID)
	if job.Status != "done" {
		t.Fatalf("expected ehbot job done, got %#v", job)
	}
	if job.Imported != 1 || job.Completed != 1 {
		t.Fatalf("unexpected ehbot counters: %#v", job)
	}

	fake.mu.Lock()
	completed := fake.completeCount
	failed := fake.failCount
	importedPath := fake.importedPath
	libraryID := fake.importedLibrary
	fake.mu.Unlock()
	if completed != 1 {
		t.Fatalf("expected 1 complete call, got %d", completed)
	}
	if failed != 0 {
		t.Fatalf("expected 0 fail call, got %d", failed)
	}
	if libraryID != "local-main" {
		t.Fatalf("unexpected imported library: %q", libraryID)
	}
	if importedPath == "" {
		t.Fatal("expected imported path from complete payload")
	}
	if _, err := os.Stat(filepath.Join(comicsRoot, filepath.FromSlash(importedPath))); err != nil {
		t.Fatalf("expected imported file: %v", err)
	}
	if filepath.Base(importedPath) != "Example Gallery Title.zip" {
		t.Fatalf("unexpected imported filename: %q", importedPath)
	}

	ids := application.LibraryComicIDs("local-main")
	if len(ids) != 1 {
		t.Fatalf("expected 1 comic after import, got %d", len(ids))
	}
	comic := application.ComicByID(ids[0])
	if comic == nil || comic.Title != "Example Gallery Title" {
		t.Fatalf("unexpected imported comic: %#v", comic)
	}
	if comic.Language != "zh" {
		t.Fatalf("unexpected language: %q", comic.Language)
	}
}

func waitForEHBotJob(t *testing.T, application *apppkg.App, jobID string) apppkg.EHBotPullJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, ok := application.EHBotJob(jobID)
		if ok && (job.Status == "done" || job.Status == "failed") {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("ehbot job %s did not finish in time", jobID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func buildEHBotArtifact(t *testing.T, files map[string][]byte) ([]byte, string) {
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

type fakeEHBotServer struct {
	t               *testing.T
	mu              sync.Mutex
	claimConsumer   string
	artifact        []byte
	artifactSHA     string
	completeCount   int
	failCount       int
	importedPath    string
	importedLibrary string
}

func newFakeEHBotServer(t *testing.T, artifact []byte, artifactSHA string) *fakeEHBotServer {
	return &fakeEHBotServer{t: t, artifact: artifact, artifactSHA: artifactSHA}
}

func (f *fakeEHBotServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pull/jobs":
		f.writeJSON(w, map[string]any{"items": []map[string]any{f.jobPayload("ready")}, "count": 1})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/claim":
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		f.claimConsumer = stringValue(payload["consumer_id"])
		f.mu.Unlock()
		f.writeJSON(w, f.jobPayload("claimed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/heartbeat":
		f.writeJSON(w, f.jobPayload("claimed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/complete":
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		f.completeCount++
		f.importedPath = stringValue(payload["imported_path"])
		f.importedLibrary = stringValue(payload["library_id"])
		f.mu.Unlock()
		f.writeJSON(w, f.jobPayload("completed"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pull/jobs/remote-job/fail":
		f.mu.Lock()
		f.failCount++
		f.mu.Unlock()
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

func (f *fakeEHBotServer) jobPayload(status string) map[string]any {
	return map[string]any{
		"job_id":    "remote-job",
		"status":    status,
		"target_id": "home-main",
		"gallery": map[string]any{
			"gid":        "3828219",
			"token":      "b71301f4cc",
			"title":      "Example Gallery Title",
			"source_url": "https://e-hentai.org/g/3828219/b71301f4cc/",
		},
		"artifact": map[string]any{
			"format":     "zip",
			"filename":   "[eh-3828219] Example Gallery Title.zip",
			"size_bytes": len(f.artifact),
			"sha256":     f.artifactSHA,
		},
	}
}

func (f *fakeEHBotServer) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

func stringValue(value any) string {
	if raw, ok := value.(string); ok {
		return raw
	}
	return ""
}