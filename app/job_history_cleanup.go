package app

import (
	"context"
	"strings"
	"time"
)

var jobRetentionByKind = map[string]int{
	"ehbot.pull": 200,
}

const (
	defaultJobRetention       = 2000
	jobHistoryCleanupInterval = 6 * time.Hour
)

func (a *App) PruneJobHistory(ctx context.Context) (int64, error) {
	if a == nil || a.metadataStore == nil {
		return 0, nil
	}
	kinds, err := a.metadataStore.DistinctJobKinds(ctx)
	if err != nil {
		return 0, err
	}
	var totalDeleted int64
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		keep := defaultJobRetention
		if value, ok := jobRetentionByKind[kind]; ok {
			keep = value
		}
		deleted, err := a.metadataStore.PruneJobsByKind(ctx, kind, keep)
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
	}
	return totalDeleted, nil
}

func (a *App) startJobHistoryCleanupService() {
	if a == nil || a.metadataStore == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.jobHistoryCleanupCancel = cancel
	go func() {
		_, _ = a.PruneJobHistory(ctx)
		ticker := time.NewTicker(jobHistoryCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = a.PruneJobHistory(ctx)
			}
		}
	}()
}

func (a *App) stopJobHistoryCleanupService() {
	if a == nil || a.jobHistoryCleanupCancel == nil {
		return
	}
	a.jobHistoryCleanupCancel()
	a.jobHistoryCleanupCancel = nil
}
