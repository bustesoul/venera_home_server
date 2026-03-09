package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

func (a *App) JobHistory(ctx context.Context, query metadatapkg.JobQuery) ([]metadatapkg.JobRecord, error) {
	if a.metadataStore == nil {
		return nil, nil
	}
	return a.metadataStore.ListJobs(ctx, query)
}

func normalizeJobTrigger(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" {
		return value
	}
	fallback = strings.ToLower(strings.TrimSpace(fallback))
	if fallback != "" {
		return fallback
	}
	return "manual"
}

func (a *App) persistJobRecord(item metadatapkg.JobRecord) {
	if a == nil || a.metadataStore == nil {
		return
	}
	if item.RequestedAt.IsZero() {
		item.RequestedAt = time.Now().UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	_ = a.metadataStore.UpsertJob(context.Background(), item)
}

func (a *App) recordAdHocJob(item metadatapkg.JobRecord) metadatapkg.JobRecord {
	if item.RequestedAt.IsZero() {
		item.RequestedAt = time.Now().UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.RequestedAt
	}
	a.persistJobRecord(item)
	return item
}

func rawJobJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == `null` || trimmed == `[]` || trimmed == `{}` {
		return nil
	}
	return json.RawMessage(raw)
}

func (a *App) syncMetadataJobHistory(job *MetadataRefreshJob) {
	if job == nil {
		return
	}
	kind := "metadata.refresh"
	if strings.TrimSpace(job.Kind) != "" {
		kind = "metadata." + strings.TrimSpace(job.Kind)
	}
	a.persistJobRecord(metadatapkg.JobRecord{
		ID:          job.ID,
		Kind:        kind,
		Trigger:     strings.TrimSpace(job.Trigger),
		Status:      strings.TrimSpace(job.Status),
		Summary:     metadataJobSummary(*job),
		LibraryID:   strings.TrimSpace(job.LibraryID),
		Path:        strings.TrimSpace(job.Path),
		Error:       strings.TrimSpace(job.Error),
		RequestedAt: job.RequestedAt,
		StartedAt:   job.StartedAt,
		FinishedAt:  job.FinishedAt,
		UpdatedAt:   metadataJobUpdatedAt(*job),
		Payload: rawJobJSON(map[string]any{
			"force":    job.Force,
			"source_id": job.SourceID,
			"state":    job.State,
			"min_score": job.MinScore,
			"workers":  job.Workers,
		}),
		Result: rawJobJSON(map[string]any{
			"processed": job.Processed,
			"examined":  job.Examined,
			"matched":   job.Matched,
			"updated":   job.Updated,
			"unmatched": job.Unmatched,
			"skipped":   job.Skipped,
			"sources":   job.Sources,
		}),
	})
}

func (a *App) syncEHBotJobHistory(job *EHBotPullJob) {
	if job == nil {
		return
	}
	a.persistJobRecord(metadatapkg.JobRecord{
		ID:          job.ID,
		Kind:        "ehbot.pull",
		Trigger:     strings.TrimSpace(job.Trigger),
		Status:      strings.TrimSpace(job.Status),
		Summary:     ehBotJobSummary(*job),
		LibraryID:   strings.TrimSpace(job.LibraryID),
		TargetID:    strings.TrimSpace(job.RemoteTargetID),
		RemoteJobID: strings.TrimSpace(job.RemoteJobID),
		Error:       strings.TrimSpace(job.Error),
		RequestedAt: job.RequestedAt,
		StartedAt:   job.StartedAt,
		FinishedAt:  job.FinishedAt,
		UpdatedAt:   ehBotJobUpdatedAt(*job),
		Payload: rawJobJSON(map[string]any{
			"last_step":   job.LastStep,
			"remote_title": job.RemoteTitle,
		}),
		Result: rawJobJSON(map[string]any{
			"listed":            job.Listed,
			"eligible":          job.Eligible,
			"claimed":           job.Claimed,
			"imported":          job.Imported,
			"completed":         job.Completed,
			"failed":            job.Failed,
			"artifact_filename": job.ArtifactFilename,
			"imported_path":     job.ImportedPath,
		}),
	})
}

func metadataJobUpdatedAt(job MetadataRefreshJob) time.Time {
	if job.FinishedAt != nil {
		return job.FinishedAt.UTC()
	}
	if job.StartedAt != nil {
		return job.StartedAt.UTC()
	}
	if !job.RequestedAt.IsZero() {
		return job.RequestedAt.UTC()
	}
	return time.Now().UTC()
}

func metadataJobSummary(job MetadataRefreshJob) string {
	parts := []string{}
	if strings.TrimSpace(job.Kind) != "" {
		parts = append(parts, strings.TrimSpace(job.Kind))
	}
	if strings.TrimSpace(job.LibraryID) != "" {
		parts = append(parts, "library="+strings.TrimSpace(job.LibraryID))
	}
	if strings.TrimSpace(job.Path) != "" {
		parts = append(parts, "path="+strings.TrimSpace(job.Path))
	}
	if strings.TrimSpace(job.State) != "" {
		parts = append(parts, "state="+strings.TrimSpace(job.State))
	}
	if strings.TrimSpace(job.SourceID) != "" {
		parts = append(parts, "source="+strings.TrimSpace(job.SourceID))
	}
	return strings.Join(parts, " | ")
}

func ehBotJobUpdatedAt(job EHBotPullJob) time.Time {
	if job.FinishedAt != nil {
		return job.FinishedAt.UTC()
	}
	if job.StartedAt != nil {
		return job.StartedAt.UTC()
	}
	if !job.RequestedAt.IsZero() {
		return job.RequestedAt.UTC()
	}
	return time.Now().UTC()
}

func ehBotJobSummary(job EHBotPullJob) string {
	parts := []string{}
	if strings.TrimSpace(job.LibraryID) != "" {
		parts = append(parts, "library="+strings.TrimSpace(job.LibraryID))
	}
	if strings.TrimSpace(job.RemoteTargetID) != "" {
		parts = append(parts, "target="+strings.TrimSpace(job.RemoteTargetID))
	}
	if strings.TrimSpace(job.RemoteTitle) != "" {
		parts = append(parts, strings.TrimSpace(job.RemoteTitle))
	}
	if strings.TrimSpace(job.ImportedPath) != "" {
		parts = append(parts, "import="+strings.TrimSpace(job.ImportedPath))
	}
	return strings.Join(parts, " | ")
}

func (a *App) CleanupMetadataTracked(ctx context.Context, req metadatapkg.CleanupRequest, trigger string) (metadatapkg.CleanupResult, error) {
	now := time.Now().UTC()
	job := metadatapkg.JobRecord{
		ID:          shared.SHAID("metadata-cleanup", req.LibraryID, fmt.Sprintf("%d", req.OlderThanDays), fmt.Sprintf("%t", req.DryRun), now.Format(time.RFC3339Nano)),
		Kind:        "metadata.cleanup",
		Trigger:     normalizeJobTrigger(trigger, "manual"),
		Status:      "running",
		Summary:     fmt.Sprintf("library=%s | older_than_days=%d | dry_run=%t", strings.TrimSpace(req.LibraryID), req.OlderThanDays, req.DryRun),
		LibraryID:   strings.TrimSpace(req.LibraryID),
		RequestedAt: now,
		StartedAt:   &now,
		UpdatedAt:   now,
		Payload:     rawJobJSON(req),
	}
	a.persistJobRecord(job)
	result, err := a.CleanupMetadata(ctx, req)
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	job.UpdatedAt = finished
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
	} else {
		job.Status = "done"
		job.Result = rawJobJSON(result)
	}
	a.persistJobRecord(job)
	return result, err
}
