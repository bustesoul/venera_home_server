package app

import (
	"context"
	"sort"
	"strings"
	"time"

	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

type MetadataRefreshRequest struct {
	LibraryID string `json:"library_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

type MetadataRefreshJob struct {
	ID         string     `json:"job_id"`
	Status     string     `json:"status"`
	LibraryID  string     `json:"library_id,omitempty"`
	Path       string     `json:"path,omitempty"`
	Force      bool       `json:"force,omitempty"`
	Processed  int        `json:"processed"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (a *App) MetadataStore() *metadatapkg.Store {
	return a.metadataStore
}

func (a *App) StartMetadataRefresh(ctx context.Context, req MetadataRefreshRequest) (MetadataRefreshJob, error) {
	if a.metadataStore == nil {
		return MetadataRefreshJob{}, nil
	}
	now := time.Now().UTC()
	job := &MetadataRefreshJob{
		ID:        shared.SHAID("metadata-refresh", req.LibraryID, req.Path, now.Format(time.RFC3339Nano)),
		Status:    "queued",
		LibraryID: req.LibraryID,
		Path:      req.Path,
		Force:     req.Force,
	}
	a.metadataJobsMu.Lock()
	if a.metadataJobs == nil {
		a.metadataJobs = map[string]*MetadataRefreshJob{}
	}
	a.metadataJobs[job.ID] = job
	a.metadataJobsMu.Unlock()

	go a.runMetadataRefreshJob(context.Background(), job)
	return *job, nil
}

func (a *App) runMetadataRefreshJob(ctx context.Context, job *MetadataRefreshJob) {
	started := time.Now().UTC()
	a.metadataJobsMu.Lock()
	job.Status = "running"
	job.StartedAt = &started
	a.metadataJobsMu.Unlock()

	err := a.Rescan(ctx, job.LibraryID)
	finished := time.Now().UTC()

	a.metadataJobsMu.Lock()
	defer a.metadataJobsMu.Unlock()
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		job.FinishedAt = &finished
		trimMetadataJobsLocked(a.metadataJobs)
		return
	}
	if strings.TrimSpace(job.LibraryID) == "" {
		job.Processed = len(a.Comics())
	} else {
		job.Processed = len(a.LibraryComicIDs(job.LibraryID))
	}
	job.Status = "done"
	job.FinishedAt = &finished
	trimMetadataJobsLocked(a.metadataJobs)
}

func trimMetadataJobsLocked(items map[string]*MetadataRefreshJob) {
	if len(items) <= 32 {
		return
	}
	type pair struct {
		id string
		at time.Time
	}
	ordered := make([]pair, 0, len(items))
	for id, item := range items {
		at := time.Time{}
		if item.FinishedAt != nil {
			at = *item.FinishedAt
		} else if item.StartedAt != nil {
			at = *item.StartedAt
		}
		ordered = append(ordered, pair{id: id, at: at})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].at.After(ordered[j].at) })
	for _, item := range ordered[32:] {
		delete(items, item.id)
	}
}

func (a *App) MetadataJob(id string) (MetadataRefreshJob, bool) {
	a.metadataJobsMu.RLock()
	defer a.metadataJobsMu.RUnlock()
	job, ok := a.metadataJobs[id]
	if !ok || job == nil {
		return MetadataRefreshJob{}, false
	}
	return *job, true
}

func (a *App) MetadataRecords(ctx context.Context, query metadatapkg.ListQuery) ([]metadatapkg.Record, error) {
	if a.metadataStore == nil {
		return nil, nil
	}
	return a.metadataStore.ListRecords(ctx, query)
}

func (a *App) CleanupMetadata(ctx context.Context, req metadatapkg.CleanupRequest) (metadatapkg.CleanupResult, error) {
	if a.metadataStore == nil {
		return metadatapkg.CleanupResult{DryRun: req.DryRun}, nil
	}
	return a.metadataStore.CleanupMissing(ctx, req)
}

func (a *App) UpdateMetadata(ctx context.Context, locator metadatapkg.Locator, update metadatapkg.Update) error {
	if a.metadataStore == nil {
		return nil
	}
	return a.metadataStore.ApplyUpdate(ctx, locator, update)
}
