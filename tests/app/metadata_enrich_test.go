package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	apppkg "venera_home_server/app"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/tests/testkit"
)

func TestMetadataEnrichmentJobAndRecordActions(t *testing.T) {
	root := t.TempDir()
	matchedRel := `[Circle] Match Book 11 [Chinese] [DL]`
	unmatchedRel := filepath.Join(`RJ01375986`, `NoText`)
	testkit.MustWriteFile(t, filepath.Join(root, matchedRel, `001.jpg`), []byte("img-a"))
	testkit.MustWriteFile(t, filepath.Join(root, unmatchedRel, `001.jpg`), []byte("img-b"))
	testkit.MustSeedExDBGallery(t, filepath.Join(root, `data`, `externaldb`, `catalog.sqlite`), []testkit.ExDBGalleryRow{
		{
			GID:      `2708021`,
			Token:    `d774d5b991`,
			Title:    `[Circle] Match Book 11`,
			TitleJPN: `[Circle] Match Book 11 [Chinese] [DL]`,
			Artist:   `["artist one"]`,
			Category: `Doujinshi`,
			Rating:   4.72,
			Thumb:    `https://ehgt.org/example-cover.webp`,
		},
		{
			GID:      `2443950`,
			Token:    `c31306ddd9`,
			Title:    `[Unrelated] SpreadBar [No Text]`,
			TitleJPN: `[Unrelated] SpreadBar [No Text]`,
			Artist:   `["someone else"]`,
			Category: `Artist CG`,
			Rating:   4.83,
			Thumb:    `https://ehgt.org/unrelated.webp`,
		},
	})

	application := newTestApp(t, root)
	job, err := application.StartMetadataEnrichment(context.Background(), apppkg.MetadataEnrichRequest{LibraryID: `local-main`, State: `empty`, Limit: 20, Workers: 2})
	if err != nil {
		t.Fatalf("StartMetadataEnrichment: %v", err)
	}
	job = waitForAppMetadataJob(t, application, job.ID)
	if job.Status != `done` {
		t.Fatalf("expected done job, got %#v", job)
	}
	if job.Updated != 1 || job.Unmatched != 1 {
		t.Fatalf("unexpected job counters: %#v", job)
	}

	matchedRecord := findMetadataRecordByRootRef(t, application, filepath.ToSlash(matchedRel))
	if matchedRecord.Source == "" || matchedRecord.SourceID != `2708021` {
		t.Fatalf("expected matched record to be enriched, got %#v", matchedRecord)
	}
	if !matchedRecord.HasConfidence || matchedRecord.MatchKind == "" {
		t.Fatalf("expected confidence and match kind, got %#v", matchedRecord)
	}
	unmatchedRecord := findMetadataRecordByRootRef(t, application, filepath.ToSlash(unmatchedRel))
	if unmatchedRecord.Source != "" || unmatchedRecord.SourceID != "" {
		t.Fatalf("expected unmatched record to stay empty, got %#v", unmatchedRecord)
	}
	matchedComic := findAppComicByRootRef(t, application, filepath.ToSlash(matchedRel))
	if matchedComic == nil || matchedComic.Title != `[Circle] Match Book 11 [Chinese] [DL]` {
		t.Fatalf("expected enriched runtime title, got %#v", matchedComic)
	}

	if _, err := application.MetadataRecordAction(context.Background(), apppkg.MetadataRecordActionRequest{Locator: locatorFromRecord(matchedRecord), Action: `lock`}); err != nil {
		t.Fatalf("lock action: %v", err)
	}
	if _, err := application.MetadataRecordAction(context.Background(), apppkg.MetadataRecordActionRequest{Locator: locatorFromRecord(matchedRecord), Action: `reset`}); err != nil {
		t.Fatalf("reset action: %v", err)
	}
	matchedRecord = findMetadataRecordByRootRef(t, application, filepath.ToSlash(matchedRel))
	if !matchedRecord.ManualLocked || matchedRecord.Source != "" || matchedRecord.Title != "" {
		t.Fatalf("expected locked record to be reset, got %#v", matchedRecord)
	}

	job, err = application.StartMetadataEnrichment(context.Background(), apppkg.MetadataEnrichRequest{LibraryID: `local-main`, State: `empty`, Limit: 20, Workers: 2})
	if err != nil {
		t.Fatalf("StartMetadataEnrichment after reset: %v", err)
	}
	job = waitForAppMetadataJob(t, application, job.ID)
	if job.Updated != 0 || job.Skipped != 0 || job.Unmatched != 1 {
		t.Fatalf("expected batch empty enrich to exclude locked records, got %#v", job)
	}

	if _, err := application.MetadataRecordAction(context.Background(), apppkg.MetadataRecordActionRequest{Locator: locatorFromRecord(matchedRecord), Action: `unlock`}); err != nil {
		t.Fatalf("unlock action: %v", err)
	}
	actionResult, err := application.MetadataRecordAction(context.Background(), apppkg.MetadataRecordActionRequest{Locator: locatorFromRecord(matchedRecord), Action: `enrich`, Workers: 1})
	if err != nil {
		t.Fatalf("single enrich action: %v", err)
	}
	if actionResult.Job == nil {
		t.Fatal("expected enrich action to create a job")
	}
	job = waitForAppMetadataJob(t, application, actionResult.Job.ID)
	if job.Updated != 1 {
		t.Fatalf("expected single enrich to update record, got %#v", job)
	}
	matchedRecord = findMetadataRecordByRootRef(t, application, filepath.ToSlash(matchedRel))
	if matchedRecord.SourceID != `2708021` || matchedRecord.Source == "" {
		t.Fatalf("expected record to be enriched again, got %#v", matchedRecord)
	}
}

func waitForAppMetadataJob(t *testing.T, application *apppkg.App, jobID string) apppkg.MetadataRefreshJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, ok := application.MetadataJob(jobID)
		if ok && (job.Status == `done` || job.Status == `failed`) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("metadata job %s did not finish in time", jobID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func findMetadataRecordByRootRef(t *testing.T, application *apppkg.App, rootRef string) metadatapkg.Record {
	t.Helper()
	records, err := application.MetadataRecords(context.Background(), metadatapkg.ListQuery{LibraryID: `local-main`, Limit: 50})
	if err != nil {
		t.Fatalf("MetadataRecords: %v", err)
	}
	for _, record := range records {
		if record.RootRef == rootRef {
			return record
		}
	}
	t.Fatalf("metadata record not found for %s", rootRef)
	return metadatapkg.Record{}
}

func findAppComicByRootRef(t *testing.T, application *apppkg.App, rootRef string) *apppkg.Comic {
	t.Helper()
	for _, id := range application.LibraryComicIDs(`local-main`) {
		comic := application.ComicByID(id)
		if comic != nil && comic.RootRef == rootRef {
			return comic
		}
	}
	t.Fatalf("comic not found for rootRef %s", rootRef)
	return nil
}

func locatorFromRecord(record metadatapkg.Record) metadatapkg.Locator {
	return metadatapkg.Locator{LibraryID: record.LibraryID, RootType: record.RootType, RootRef: record.RootRef}
}
