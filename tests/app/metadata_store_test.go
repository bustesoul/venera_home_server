package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
)

func TestMetadataStoreFillsOnlyMissingFields(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("img"))
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "ComicInfo.xml"), []byte(`<ComicInfo><Title>Local Title</Title><Writer>Local Author</Writer></ComicInfo>`))

	application := newTestApp(t, root)
	comic := findAppComicByTitle(t, application, "local-main", "Local Title")
	locator := metadatapkg.Locator{LibraryID: comic.LibraryID, RootType: comic.RootType, RootRef: comic.RootRef}
	if err := application.UpdateMetadata(context.Background(), locator, metadatapkg.Update{
		Title:       "Remote Title",
		Subtitle:    "Remote Subtitle",
		Description: "Remote Description",
		Artists:     []string{"Remote Author"},
		Tags:        []string{"tag-a", "tag-b"},
		Language:    "ja",
		SourceURL:   "https://example.com/gallery/1",
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	updated := application.ComicByID(comic.ID)
	if updated == nil {
		t.Fatal("expected comic after rescan")
	}
	if updated.Title != "Local Title" {
		t.Fatalf("expected local title to win, got %q", updated.Title)
	}
	if updated.Subtitle != "Remote Subtitle" {
		t.Fatalf("expected remote subtitle fill, got %q", updated.Subtitle)
	}
	if updated.Description != "Remote Description" {
		t.Fatalf("expected remote description fill, got %q", updated.Description)
	}
	if len(updated.Authors) != 1 || updated.Authors[0] != "Local Author" {
		t.Fatalf("expected local authors to stay intact, got %#v", updated.Authors)
	}
	if len(updated.Tags) != 2 || updated.Tags[0] != "tag-a" || updated.Tags[1] != "tag-b" {
		t.Fatalf("expected remote tags fill, got %#v", updated.Tags)
	}
	if updated.Language != "ja" {
		t.Fatalf("expected remote language fill, got %q", updated.Language)
	}
	if updated.SourceURL != "https://example.com/gallery/1" {
		t.Fatalf("expected remote source url fill, got %q", updated.SourceURL)
	}
}

func TestMetadataStoreRebindsMovedComicByFingerprint(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "Shelf", "Book A")
	newDir := filepath.Join(root, "Shelf", "Book B")
	testkit.MustWriteFile(t, filepath.Join(oldDir, "001.jpg"), []byte("img-1"))
	testkit.MustWriteFile(t, filepath.Join(oldDir, "002.jpg"), []byte("img-2"))

	application := newTestApp(t, root)
	comic := findAppComicByTitle(t, application, "local-main", "Book A")
	locator := metadatapkg.Locator{LibraryID: comic.LibraryID, RootType: comic.RootType, RootRef: comic.RootRef}
	if err := application.UpdateMetadata(context.Background(), locator, metadatapkg.Update{
		Title:       "Persisted Title",
		Description: "Persisted Description",
	}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := application.Rescan(context.Background(), "local-main"); err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	expectedRootRef := filepath.ToSlash(filepath.Join("Shelf", "Book B"))
	retitled := findAppComicByRootRef(t, application, expectedRootRef)
	if retitled.RootRef != expectedRootRef {
		t.Fatalf("expected rebound root_ref %q, got %q", expectedRootRef, retitled.RootRef)
	}
	if retitled.Title != "Persisted Title" {
		records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: "local-main", Limit: 10})
		if err != nil {
			t.Fatalf("MetadataRecords on mismatch: %v", err)
		}
		t.Logf("records on mismatch=%#v", records)
		t.Fatalf("expected rebound title %q, got %q", "Persisted Title", retitled.Title)
	}
	if retitled.Description != "Persisted Description" {
		t.Fatalf("expected rebound description, got %q", retitled.Description)
	}
	records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: "local-main", Limit: 10})
	if err != nil {
		t.Fatalf("MetadataRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 metadata record after rebind, got %d", len(records))
	}
	if records[0].RootRef != expectedRootRef {
		t.Fatalf("expected rebound record root_ref %q, got %q", expectedRootRef, records[0].RootRef)
	}
	if records[0].MissingSince != nil {
		t.Fatalf("expected rebound record to be active, got missing_since=%v", records[0].MissingSince)
	}
}

func TestStartupMarksUnfinishedJobsFailed(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("img"))

	cfg := &configpkg.Config{
		Server:    configpkg.ServerConfig{Listen: "127.0.0.1:0", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      configpkg.ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  configpkg.MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []configpkg.LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: root, ScanMode: "auto"}},
	}
	store, err := metadatapkg.OpenStore(cfg.Server.DataDir, cfg.Metadata.DatabasePath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	if err := store.UpsertJob(context.Background(), metadatapkg.JobRecord{
		ID:          "orphan-running-job",
		Kind:        "metadata.refresh",
		Trigger:     "startup",
		Status:      "running",
		RequestedAt: started,
		StartedAt:   &started,
		UpdatedAt:   started,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("UpsertJob: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	application, err := apppkg.NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	jobs, err := application.JobHistory(context.Background(), metadatapkg.JobQuery{Kind: "metadata.refresh", Limit: 20})
	if err != nil {
		t.Fatalf("JobHistory: %v", err)
	}
	foundOrphan := false
	foundStartupDone := false
	for _, job := range jobs {
		if job.ID == "orphan-running-job" {
			foundOrphan = true
			if job.Status != "failed" {
				t.Fatalf("expected orphan job failed, got %#v", job)
			}
			if job.Error != "server restarted before job finished" {
				t.Fatalf("unexpected orphan job error: %#v", job)
			}
			if job.FinishedAt == nil {
				t.Fatalf("expected orphan job finished_at, got %#v", job)
			}
		}
		if job.Trigger == "startup" && job.Status == "done" {
			foundStartupDone = true
		}
	}
	if !foundOrphan {
		t.Fatalf("expected orphan-running-job in history, got %#v", jobs)
	}
	if !foundStartupDone {
		t.Fatalf("expected a completed startup metadata.refresh job, got %#v", jobs)
	}
}

func TestPruneJobHistoryUsesPerKindRetention(t *testing.T) {
	root := t.TempDir()
	testkit.MustWriteFile(t, filepath.Join(root, "Book A", "001.jpg"), []byte("img"))

	application := newTestApp(t, root)
	t.Cleanup(func() { _ = application.Close() })

	store := application.MetadataStore()
	baseTime := time.Now().UTC().Add(-3 * time.Hour)

	for i := 0; i < 205; i++ {
		at := baseTime.Add(time.Duration(i) * time.Second)
		if err := store.UpsertJob(context.Background(), metadatapkg.JobRecord{
			ID:          fmt.Sprintf("ehbot-%03d", i),
			Kind:        "ehbot.pull",
			Trigger:     "test",
			Status:      "done",
			RequestedAt: at,
			UpdatedAt:   at,
		}); err != nil {
			t.Fatalf("UpsertJob ehbot.pull[%d]: %v", i, err)
		}
	}

	for i := 0; i < 2005; i++ {
		at := baseTime.Add(time.Duration(i) * time.Second)
		if err := store.UpsertJob(context.Background(), metadatapkg.JobRecord{
			ID:          fmt.Sprintf("metadata-%04d", i),
			Kind:        "custom.kind",
			Trigger:     "test",
			Status:      "done",
			RequestedAt: at,
			UpdatedAt:   at,
		}); err != nil {
			t.Fatalf("UpsertJob metadata.refresh[%d]: %v", i, err)
		}
	}

	deleted, err := application.PruneJobHistory(context.Background())
	if err != nil {
		t.Fatalf("PruneJobHistory: %v", err)
	}
	if deleted != 10 {
		t.Fatalf("expected 10 deleted rows, got %d", deleted)
	}

	ehbotPage, err := application.JobHistoryPage(context.Background(), metadatapkg.JobQuery{Kind: "ehbot.pull", Limit: 1})
	if err != nil {
		t.Fatalf("JobHistoryPage ehbot.pull total: %v", err)
	}
	if ehbotPage.Total != 200 {
		t.Fatalf("expected 200 ehbot.pull jobs after prune, got %d", ehbotPage.Total)
	}
	lastEHBot, err := application.JobHistoryPage(context.Background(), metadatapkg.JobQuery{Kind: "ehbot.pull", Limit: 1, Offset: 199})
	if err != nil {
		t.Fatalf("JobHistoryPage ehbot.pull last kept: %v", err)
	}
	if len(lastEHBot.Items) != 1 || lastEHBot.Items[0].ID != "ehbot-005" {
		t.Fatalf("expected oldest kept ehbot job ehbot-005, got %#v", lastEHBot.Items)
	}

	metadataPage, err := application.JobHistoryPage(context.Background(), metadatapkg.JobQuery{Kind: "custom.kind", Limit: 1})
	if err != nil {
		t.Fatalf("JobHistoryPage custom.kind total: %v", err)
	}
	if metadataPage.Total != 2000 {
		t.Fatalf("expected 2000 custom.kind jobs after prune, got %d", metadataPage.Total)
	}
	lastMetadata, err := application.JobHistoryPage(context.Background(), metadatapkg.JobQuery{Kind: "custom.kind", Limit: 1, Offset: 1999})
	if err != nil {
		t.Fatalf("JobHistoryPage custom.kind last kept: %v", err)
	}
	if len(lastMetadata.Items) != 1 || lastMetadata.Items[0].ID != "metadata-0005" {
		t.Fatalf("expected oldest kept custom.kind job metadata-0005, got %#v", lastMetadata.Items)
	}
}

func findAppComicByTitle(t *testing.T, application *apppkg.App, libraryID, title string) *apppkg.Comic {
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
