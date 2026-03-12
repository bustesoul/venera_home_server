package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultCacheCleanupIntervalMinutes = 360
	minCacheCleanupIntervalMinutes     = 5
)

type CacheCleanupResult struct {
	CheckedFiles int   `json:"checked_files"`
	RemovedFiles int   `json:"removed_files"`
	FreedBytes   int64 `json:"freed_bytes"`
}

func (a *App) CleanupCacheNow() (CacheCleanupResult, error) {
	if a == nil || a.cfg == nil {
		return CacheCleanupResult{}, nil
	}
	maxAge := time.Duration(normalizedCacheMaxAgeHours(a.cfg.Server.CacheMaxAgeHours)) * time.Hour
	if maxAge <= 0 {
		return CacheCleanupResult{}, nil
	}
	cutoff := time.Now().Add(-maxAge)
	result := CacheCleanupResult{}
	for _, root := range cacheCleanupRoots(a.cfg.Server.CacheDir) {
		itemResult, err := cleanupCacheTree(root, cutoff)
		result.CheckedFiles += itemResult.CheckedFiles
		result.RemovedFiles += itemResult.RemovedFiles
		result.FreedBytes += itemResult.FreedBytes
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (a *App) startCacheCleanupService() {
	if a == nil || a.cfg == nil {
		return
	}
	maxAgeHours := normalizedCacheMaxAgeHours(a.cfg.Server.CacheMaxAgeHours)
	if maxAgeHours <= 0 {
		return
	}
	interval := time.Duration(normalizedCacheCleanupIntervalMinutes(a.cfg.Server.CacheCleanupIntervalMinutes)) * time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	a.cacheCleanupCancel = cancel
	go func() {
		_, _ = a.CleanupCacheNow()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = a.CleanupCacheNow()
			}
		}
	}()
}

func (a *App) stopCacheCleanupService() {
	if a == nil || a.cacheCleanupCancel == nil {
		return
	}
	a.cacheCleanupCancel()
	a.cacheCleanupCancel = nil
}

func normalizedCacheMaxAgeHours(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func normalizedCacheCleanupIntervalMinutes(value int) int {
	if value <= 0 {
		return defaultCacheCleanupIntervalMinutes
	}
	if value < minCacheCleanupIntervalMinutes {
		return minCacheCleanupIntervalMinutes
	}
	return value
}

func cacheCleanupRoots(cacheDir string) []string {
	return []string{
		filepath.Join(cacheDir, "rendered-pages"),
		filepath.Join(cacheDir, "archive-source"),
		filepath.Join(cacheDir, "pdf"),
		filepath.Join(cacheDir, "webdav"),
	}
}

func cleanupCacheTree(root string, cutoff time.Time) (CacheCleanupResult, error) {
	result := CacheCleanupResult{}
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, nil
	}
	dirs := []string{}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			dirs = append(dirs, current)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.CheckedFiles++
		if info.ModTime().After(cutoff) {
			return nil
		}
		result.FreedBytes += info.Size()
		result.RemovedFiles++
		if err := os.Remove(current); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for i := len(dirs) - 1; i >= 1; i-- {
		_ = os.Remove(dirs[i])
	}
	return result, nil
}
