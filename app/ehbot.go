package app

import (
	"context"
	backendpkg "venera_home_server/backend"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

const ehBotJobHistoryLimit = 64

type EHBotPullJob struct {
	ID               string     `json:"job_id"`
	Trigger          string     `json:"trigger,omitempty"`
	Status           string     `json:"status"`
	LibraryID        string     `json:"library_id,omitempty"`
	Listed           int        `json:"listed"`
	Eligible         int        `json:"eligible"`
	Claimed          int        `json:"claimed"`
	Imported         int        `json:"imported"`
	Completed        int        `json:"completed"`
	Failed           int        `json:"failed"`
	LastStep         string     `json:"last_step,omitempty"`
	RemoteJobID      string     `json:"remote_job_id,omitempty"`
	RemoteTitle      string     `json:"remote_title,omitempty"`
	RemoteTargetID   string     `json:"remote_target_id,omitempty"`
	ArtifactFilename string     `json:"artifact_filename,omitempty"`
	ImportedPath     string     `json:"imported_path,omitempty"`
	Error            string     `json:"error,omitempty"`
	RequestedAt      time.Time  `json:"requested_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

type EHBotCreateRequest struct {
	Input   string `json:"input"`
	TargetID string `json:"target_id,omitempty"`
	Trigger string `json:"trigger,omitempty"`
}

type EHBotCreateResult struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status,omitempty"`
	TargetID  string `json:"target_id,omitempty"`
	Title     string `json:"title,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
}

type EHBotRuntimeState struct {
	Running          bool
	CurrentJobID     string
	LastPollAt       *time.Time
	LastSuccessAt    *time.Time
	LastError        string
	LastRemoteJobID  string
	LastImportedPath string
}

type EHBotStatus struct {
	Enabled                bool       `json:"enabled"`
	Configured             bool       `json:"configured"`
	ConfigError            string     `json:"config_error,omitempty"`
	BaseURL                string     `json:"base_url,omitempty"`
	ConsumerID             string     `json:"consumer_id,omitempty"`
	TargetID               string     `json:"target_id,omitempty"`
	TargetLibraryID        string     `json:"target_library_id,omitempty"`
	TargetLibraryName      string     `json:"target_library_name,omitempty"`
	TargetLibraryKind      string     `json:"target_library_kind,omitempty"`
	TargetSubdir           string     `json:"target_subdir,omitempty"`
	PollIntervalSeconds    int        `json:"poll_interval_seconds"`
	LeaseSeconds           int        `json:"lease_seconds"`
	DownloadTimeoutSeconds int        `json:"download_timeout_seconds"`
	AutoRescan             bool       `json:"auto_rescan"`
	MaxJobsPerPoll         int        `json:"max_jobs_per_poll"`
	Running                bool       `json:"running"`
	QueueDepth             int        `json:"queue_depth"`
	CurrentJobID           string     `json:"current_job_id,omitempty"`
	LastPollAt             *time.Time `json:"last_poll_at,omitempty"`
	LastSuccessAt          *time.Time `json:"last_success_at,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	LastRemoteJobID        string     `json:"last_remote_job_id,omitempty"`
	LastImportedPath       string     `json:"last_imported_path,omitempty"`
}

type ehBotRemoteJob struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	TargetID string `json:"target_id,omitempty"`
	Gallery  struct {
		GID       string   `json:"gid"`
		Token     string   `json:"token"`
		Title     string   `json:"title,omitempty"`
		Uploader  string   `json:"uploader,omitempty"`
		Tags      []string `json:"tags,omitempty"`
		SourceURL string   `json:"source_url,omitempty"`
	} `json:"gallery"`
	Artifact struct {
		Format    string `json:"format,omitempty"`
		Filename  string `json:"filename,omitempty"`
		SizeBytes int64  `json:"size_bytes,omitempty"`
		SHA256    string `json:"sha256,omitempty"`
	} `json:"artifact,omitempty"`
	Claim *struct {
		ConsumerID string `json:"consumer_id,omitempty"`
	} `json:"claim,omitempty"`
}

type ehBotClient struct {
	baseURL    string
	pullToken  string
	consumerID string
	httpClient *http.Client
}

type ehBotImportResult struct {
	RelPath string
	SHA256  string
}

func (a *App) startEHBotService() {
	ctx, cancel := context.WithCancel(context.Background())
	a.ehBotMu.Lock()
	a.ehBotQueue = make(chan *EHBotPullJob, 8)
	a.ehBotCancel = cancel
	a.ehBotMu.Unlock()
	go a.runEHBotWorker(ctx)
	if normalizeEHBotConfig(a.cfg.EHBot).Enabled {
		go a.runEHBotPoller(ctx)
	}
}

func (a *App) stopEHBotService() {
	a.ehBotMu.RLock()
	cancel := a.ehBotCancel
	a.ehBotMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) runEHBotWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-a.ehBotQueue:
			if job == nil {
				continue
			}
			a.runEHBotPullJob(ctx, job)
		}
	}
}

func (a *App) runEHBotPoller(ctx context.Context) {
	a.tryEnqueueAutoEHBotPull("startup")
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tryEnqueueAutoEHBotPull("auto")
		}
	}
}

func (a *App) StartEHBotPull(_ context.Context) (EHBotPullJob, error) {
	return a.enqueueEHBotPull("manual")
}

func (a *App) CreateEHBotRemoteJob(ctx context.Context, req EHBotCreateRequest) (EHBotCreateResult, error) {
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return EHBotCreateResult{}, fmt.Errorf("input is required")
	}
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	lib, found := a.libraryConfig(cfg.TargetLibraryID)
	if configError := ehBotConfigError(cfg, lib, found); configError != "" {
		return EHBotCreateResult{}, fmt.Errorf(configError)
	}
	client, err := newEHBotClient(cfg)
	if err != nil {
		return EHBotCreateResult{}, err
	}
	targetID := strings.TrimSpace(req.TargetID)
	if targetID == "" {
		targetID = cfg.TargetID
	}
	remoteJob, endpoint, err := client.createDownloadJob(ctx, targetID, input)
	if err != nil {
		return EHBotCreateResult{}, err
	}
	result := EHBotCreateResult{
		JobID:     strings.TrimSpace(remoteJob.JobID),
		Status:    strings.TrimSpace(remoteJob.Status),
		TargetID:  strings.TrimSpace(remoteJob.TargetID),
		Title:     strings.TrimSpace(remoteJob.Gallery.Title),
		SourceURL: strings.TrimSpace(remoteJob.Gallery.SourceURL),
	}
	if result.TargetID == "" {
		result.TargetID = targetID
	}
	now := time.Now().UTC()
	status := result.Status
	if status == "" {
		status = "accepted"
	}
	a.recordAdHocJob(metadatapkg.JobRecord{
		ID:          shared.SHAID("ehbot-create", result.JobID, now.Format(time.RFC3339Nano)),
		Kind:        "ehbot.create_remote",
		Trigger:     normalizeJobTrigger(req.Trigger, "manual"),
		Status:      status,
		Summary:     ehBotCreateSummary(result, input),
		LibraryID:   strings.TrimSpace(cfg.TargetLibraryID),
		TargetID:    result.TargetID,
		RemoteJobID: result.JobID,
		RequestedAt: now,
		UpdatedAt:   now,
		Payload: rawJobJSON(map[string]any{
			"endpoint": endpoint,
			"input":    input,
			"target_id": targetID,
		}),
		Result: rawJobJSON(result),
	})
	return result, nil
}

func (a *App) enqueueEHBotPull(trigger string) (EHBotPullJob, error) {
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	now := time.Now().UTC()
	job := &EHBotPullJob{
		ID:          shared.SHAID("ehbot-pull", trigger, now.Format(time.RFC3339Nano)),
		Trigger:     trigger,
		Status:      "queued",
		LibraryID:   cfg.TargetLibraryID,
		RequestedAt: now,
	}
	a.ehBotMu.Lock()
	if a.ehBotQueue == nil {
		a.ehBotQueue = make(chan *EHBotPullJob, 8)
	}
	a.ehBotJobs[job.ID] = job
	trimEHBotJobsLocked(a.ehBotJobs)
	queue := a.ehBotQueue
	a.ehBotMu.Unlock()
	a.syncEHBotJobHistory(job)
	select {
	case queue <- job:
		return *job, nil
	default:
		finished := time.Now().UTC()
		a.ehBotMu.Lock()
		job.Status = "failed"
		job.Error = "ehbot worker queue is full"
		job.FinishedAt = &finished
		a.ehBotJobs[job.ID] = job
		a.ehBotState.LastError = job.Error
		a.ehBotMu.Unlock()
		a.syncEHBotJobHistory(job)
		return *job, fmt.Errorf("ehbot worker queue is full")
	}
}

func (a *App) tryEnqueueAutoEHBotPull(trigger string) {
	a.ehBotMu.RLock()
	busy := a.ehBotState.Running
	queueDepth := 0
	if a.ehBotQueue != nil {
		queueDepth = len(a.ehBotQueue)
	}
	a.ehBotMu.RUnlock()
	if busy || queueDepth > 0 {
		return
	}
	_, _ = a.enqueueEHBotPull(trigger)
}

func (a *App) EHBotStatus() EHBotStatus {
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	lib, found := a.libraryConfig(cfg.TargetLibraryID)
	configError := ehBotConfigError(cfg, lib, found)
	a.ehBotMu.RLock()
	defer a.ehBotMu.RUnlock()
	queueDepth := 0
	if a.ehBotQueue != nil {
		queueDepth = len(a.ehBotQueue)
	}
	return EHBotStatus{
		Enabled:                cfg.Enabled,
		Configured:             configError == "",
		ConfigError:            configError,
		BaseURL:                cfg.BaseURL,
		ConsumerID:             cfg.ConsumerID,
		TargetID:               cfg.TargetID,
		TargetLibraryID:        cfg.TargetLibraryID,
		TargetLibraryName:      lib.Name,
		TargetLibraryKind:      lib.Kind,
		TargetSubdir:           cfg.TargetSubdir,
		PollIntervalSeconds:    cfg.PollIntervalSeconds,
		LeaseSeconds:           cfg.LeaseSeconds,
		DownloadTimeoutSeconds: cfg.DownloadTimeoutSeconds,
		AutoRescan:             cfg.AutoRescan,
		MaxJobsPerPoll:         cfg.MaxJobsPerPoll,
		Running:                a.ehBotState.Running,
		QueueDepth:             queueDepth,
		CurrentJobID:           a.ehBotState.CurrentJobID,
		LastPollAt:             cloneTimePtr(a.ehBotState.LastPollAt),
		LastSuccessAt:          cloneTimePtr(a.ehBotState.LastSuccessAt),
		LastError:              a.ehBotState.LastError,
		LastRemoteJobID:        a.ehBotState.LastRemoteJobID,
		LastImportedPath:       a.ehBotState.LastImportedPath,
	}
}

func (a *App) EHBotJobs() []EHBotPullJob {
	a.ehBotMu.RLock()
	defer a.ehBotMu.RUnlock()
	items := make([]EHBotPullJob, 0, len(a.ehBotJobs))
	for _, job := range a.ehBotJobs {
		if job == nil {
			continue
		}
		items = append(items, *job)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].RequestedAt.Equal(items[j].RequestedAt) {
			return items[i].RequestedAt.After(items[j].RequestedAt)
		}
		return items[i].ID > items[j].ID
	})
	return items
}

func (a *App) EHBotJob(id string) (EHBotPullJob, bool) {
	a.ehBotMu.RLock()
	defer a.ehBotMu.RUnlock()
	job, ok := a.ehBotJobs[id]
	if !ok || job == nil {
		return EHBotPullJob{}, false
	}
	return *job, true
}

func (a *App) runEHBotPullJob(ctx context.Context, job *EHBotPullJob) {
	started := time.Now().UTC()
	a.ehBotMu.Lock()
	job.Status = "running"
	job.StartedAt = &started
	job.LastStep = "listing ready jobs"
	a.ehBotState.Running = true
	a.ehBotState.CurrentJobID = job.ID
	a.ehBotState.LastPollAt = cloneTimePtr(job.StartedAt)
	a.ehBotMu.Unlock()
	a.syncEHBotJobHistory(job)

	err := a.executeEHBotPull(ctx, job)
	finished := time.Now().UTC()

	a.ehBotMu.Lock()
	job.FinishedAt = &finished
	a.ehBotState.Running = false
	a.ehBotState.CurrentJobID = ""
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		a.ehBotState.LastError = err.Error()
		trimEHBotJobsLocked(a.ehBotJobs)
		a.ehBotMu.Unlock()
		a.syncEHBotJobHistory(job)
		return
	}
	job.Status = "done"
	job.LastStep = "done"
	a.ehBotState.LastError = ""
	lastSuccess := finished
	a.ehBotState.LastSuccessAt = &lastSuccess
	trimEHBotJobsLocked(a.ehBotJobs)
	a.ehBotMu.Unlock()
	a.syncEHBotJobHistory(job)
}

func (a *App) executeEHBotPull(ctx context.Context, job *EHBotPullJob) error {
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	lib, found := a.libraryConfig(cfg.TargetLibraryID)
	if configError := ehBotConfigError(cfg, lib, found); configError != "" {
		return fmt.Errorf(configError)
	}
	client, err := newEHBotClient(cfg)
	if err != nil {
		return err
	}
	jobs, err := client.listReadyJobs(ctx, cfg.MaxJobsPerPoll)
	if err != nil {
		return err
	}
	eligible := make([]ehBotRemoteJob, 0, len(jobs))
	for _, remoteJob := range jobs {
		if !ehBotTargetMatches(cfg.TargetID, remoteJob.TargetID) {
			continue
		}
		eligible = append(eligible, remoteJob)
	}
	a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
		item.Listed = len(jobs)
		item.Eligible = len(eligible)
		item.LastStep = "claiming remote jobs"
	})
	importedCount := 0
	for _, remoteJob := range eligible {
		a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
			item.RemoteJobID = remoteJob.JobID
			item.RemoteTitle = remoteJob.Gallery.Title
			item.RemoteTargetID = remoteJob.TargetID
			item.ArtifactFilename = remoteJob.Artifact.Filename
			item.LastStep = fmt.Sprintf("claiming %s", remoteJob.JobID)
		})
		claimedJob, err := client.claim(ctx, remoteJob.JobID, cfg.ConsumerID, cfg.LeaseSeconds)
		if err != nil {
			continue
		}
		a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
			item.Claimed++
			item.LastStep = fmt.Sprintf("downloading %s", claimedJob.JobID)
		})
		stopHeartbeat := startEHBotHeartbeat(ctx, client, claimedJob.JobID, cfg.ConsumerID, cfg.LeaseSeconds)
		importResult, importErr := a.importEHBotArtifact(ctx, client, cfg, lib, claimedJob)
		stopHeartbeat()
		if importErr != nil {
			_, _ = client.fail(ctx, claimedJob.JobID, "IMPORT_FAILED", importErr.Error(), true)
			a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
				item.Failed++
				item.Error = importErr.Error()
				item.LastStep = fmt.Sprintf("remote fail %s", claimedJob.JobID)
			})
			continue
		}
		if err := client.complete(ctx, claimedJob.JobID, cfg.ConsumerID, lib.ID, importResult.RelPath, importResult.SHA256); err != nil {
			return fmt.Errorf("complete %s: %w", claimedJob.JobID, err)
		}
		importedCount++
		a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
			item.Imported++
			item.Completed++
			item.ImportedPath = importResult.RelPath
			item.LastStep = fmt.Sprintf("completed %s", claimedJob.JobID)
		})
		a.ehBotMu.Lock()
		a.ehBotState.LastRemoteJobID = claimedJob.JobID
		a.ehBotState.LastImportedPath = importResult.RelPath
		a.ehBotMu.Unlock()
	}
	if importedCount > 0 && cfg.AutoRescan {
		a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
			item.LastStep = fmt.Sprintf("rescanning library %s", lib.ID)
		})
		if err := a.Rescan(ctx, lib.ID); err != nil {
			a.updateEHBotJob(job.ID, func(item *EHBotPullJob) {
				item.Error = fmt.Sprintf("import completed, but rescan failed: %v", err)
			})
		}
	}
	return nil
}

func (a *App) importEHBotArtifact(ctx context.Context, client *ehBotClient, cfg configpkg.EHBotConfig, lib configpkg.LibraryConfig, remoteJob ehBotRemoteJob) (ehBotImportResult, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.DownloadTimeoutSeconds)*time.Second)
	defer cancel()
	resp, err := client.downloadArtifact(downloadCtx, remoteJob.JobID)
	if err != nil {
		return ehBotImportResult{}, err
	}
	defer resp.Body.Close()
	filename := safeEHBotArtifactFilename(remoteJob.Artifact.Filename)
	stageRelPath := shared.RelJoin(cfg.TargetSubdir, fmt.Sprintf("ehbot_%s_%s", remoteJob.JobID, filename))
	stageAbsPath := filepath.Join(lib.Root, filepath.FromSlash(stageRelPath))
	tempPath := stageAbsPath + ".part"
	if err := os.MkdirAll(filepath.Dir(stageAbsPath), 0o755); err != nil {
		return ehBotImportResult{}, err
	}
	_ = os.Remove(tempPath)
	file, err := os.Create(tempPath)
	if err != nil {
		return ehBotImportResult{}, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hasher), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return ehBotImportResult{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return ehBotImportResult{}, closeErr
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	expectedSHA := strings.TrimSpace(resp.Header.Get("X-Artifact-SHA256"))
	if expectedSHA == "" {
		expectedSHA = strings.TrimSpace(remoteJob.Artifact.SHA256)
	}
	if expectedSHA != "" && !strings.EqualFold(expectedSHA, sum) {
		_ = os.Remove(tempPath)
		return ehBotImportResult{}, fmt.Errorf("artifact checksum mismatch")
	}
	_ = os.Remove(stageAbsPath)
	if err := os.Rename(tempPath, stageAbsPath); err != nil {
		_ = os.Remove(tempPath)
		return ehBotImportResult{}, err
	}
	finalRelPath, err := a.finalizeEHBotImportedArtifact(ctx, lib, stageRelPath, filename, remoteJob)
	if err != nil {
		return ehBotImportResult{}, err
	}
	return ehBotImportResult{RelPath: finalRelPath, SHA256: sum}, nil
}

func (a *App) updateEHBotJob(id string, update func(*EHBotPullJob)) {
	a.ehBotMu.Lock()
	job, ok := a.ehBotJobs[id]
	if !ok || job == nil || update == nil {
		a.ehBotMu.Unlock()
		return
	}
	update(job)
	a.ehBotMu.Unlock()
	a.syncEHBotJobHistory(job)
}

func trimEHBotJobsLocked(items map[string]*EHBotPullJob) {
	if len(items) <= ehBotJobHistoryLimit {
		return
	}
	type pair struct {
		id string
		at time.Time
	}
	ordered := make([]pair, 0, len(items))
	for id, job := range items {
		if job == nil {
			continue
		}
		at := job.RequestedAt
		if job.FinishedAt != nil {
			at = *job.FinishedAt
		} else if job.StartedAt != nil {
			at = *job.StartedAt
		}
		ordered = append(ordered, pair{id: id, at: at})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].at.After(ordered[j].at) })
	for _, item := range ordered[ehBotJobHistoryLimit:] {
		delete(items, item.id)
	}
}

func ehBotCreateSummary(result EHBotCreateResult, input string) string {
	parts := []string{"remote-create"}
	if strings.TrimSpace(result.TargetID) != "" {
		parts = append(parts, "target="+strings.TrimSpace(result.TargetID))
	}
	if strings.TrimSpace(result.Title) != "" {
		parts = append(parts, strings.TrimSpace(result.Title))
	} else if strings.TrimSpace(input) != "" {
		parts = append(parts, strings.TrimSpace(input))
	}
	return strings.Join(parts, " | ")
}

func normalizeEHBotConfig(cfg configpkg.EHBotConfig) configpkg.EHBotConfig {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.PullToken = strings.TrimSpace(cfg.PullToken)
	cfg.TargetID = strings.TrimSpace(cfg.TargetID)
	cfg.TargetLibraryID = strings.TrimSpace(cfg.TargetLibraryID)
	cfg.TargetSubdir = strings.Trim(strings.TrimSpace(strings.ReplaceAll(cfg.TargetSubdir, "\\", "/")), "/")
	if cfg.ConsumerID == "" {
		cfg.ConsumerID = defaultEHBotConsumerID(cfg.TargetLibraryID)
	} else {
		cfg.ConsumerID = strings.TrimSpace(cfg.ConsumerID)
	}
	if cfg.PollIntervalSeconds <= 0 {
		cfg.PollIntervalSeconds = 60
	}
	if cfg.LeaseSeconds <= 0 {
		cfg.LeaseSeconds = 1800
	}
	if cfg.DownloadTimeoutSeconds <= 0 {
		cfg.DownloadTimeoutSeconds = 1800
	}
	if cfg.MaxJobsPerPoll <= 0 {
		cfg.MaxJobsPerPoll = 1
	}
	return cfg
}

func defaultEHBotConsumerID(targetLibraryID string) string {
	hostname, err := os.Hostname()
	if err == nil {
		hostname = strings.TrimSpace(hostname)
	}
	if hostname == "" {
		hostname = "venera-home"
	}
	targetLibraryID = strings.TrimSpace(targetLibraryID)
	if targetLibraryID == "" {
		return hostname
	}
	return hostname + "-" + targetLibraryID
}

func ehBotConfigError(cfg configpkg.EHBotConfig, lib configpkg.LibraryConfig, found bool) string {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return "ehbot.base_url is required"
	}
	if _, err := url.ParseRequestURI(cfg.BaseURL); err != nil {
		return fmt.Sprintf("ehbot.base_url is invalid: %v", err)
	}
	if strings.TrimSpace(cfg.TargetLibraryID) == "" {
		return "ehbot.target_library_id is required"
	}
	if !found {
		return fmt.Sprintf("target library %q not found", cfg.TargetLibraryID)
	}
	if strings.ToLower(strings.TrimSpace(lib.Kind)) != "local" {
		return fmt.Sprintf("target library %q must be local for ehbot import", lib.ID)
	}
	if strings.TrimSpace(lib.Root) == "" {
		return fmt.Sprintf("target library %q root is empty", lib.ID)
	}
	return ""
}

func ehBotTargetMatches(expected string, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	if expected == "" {
		return actual == ""
	}
	return expected == actual
}

func safeEHBotArtifactFilename(name string) string {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if base == "" || base == "." || base == "/" {
		base = "artifact.zip"
	}
	var builder strings.Builder
	for _, r := range base {
		switch {
		case r < 32:
			builder.WriteRune('_')
		case strings.ContainsRune(`<>:"/\\|?*`, r):
			builder.WriteRune('_')
		default:
			builder.WriteRune(r)
		}
	}
	cleaned := strings.Trim(strings.TrimSpace(builder.String()), ".")
	if cleaned == "" {
		cleaned = "artifact.zip"
	}
	if filepath.Ext(cleaned) == "" {
		cleaned += ".zip"
	}
	return cleaned
}

func (a *App) finalizeEHBotImportedArtifact(ctx context.Context, lib configpkg.LibraryConfig, stageRelPath string, originalFilename string, remoteJob ehBotRemoteJob) (string, error) {
	backend := backendpkg.NewLocalBackend(lib.Root)
	fallbackTitle := firstNonEmpty(strings.TrimSpace(remoteJob.Gallery.Title), shared.BaseNameTitle(originalFilename), shared.BaseNameTitle(stageRelPath))
	meta, _, err := a.loadMetadataForArchive(ctx, backend, stageRelPath, fallbackTitle)
	title := fallbackTitle
	if err == nil {
		title = firstNonEmpty(strings.TrimSpace(meta.Title), strings.TrimSpace(meta.Series), title)
	}
	ext := strings.ToLower(strings.TrimSpace(path.Ext(originalFilename)))
	if ext == "" {
		ext = strings.ToLower(strings.TrimSpace(path.Ext(stageRelPath)))
	}
	if ext == "" {
		ext = ".zip"
	}
	finalFilename := buildEHBotStoredFilename(title, ext)
	dirRelPath := path.Dir(stageRelPath)
	if dirRelPath == "." || dirRelPath == "/" {
		dirRelPath = ""
	}
	finalRelPath, err := nextAvailableEHBotArtifactPath(lib.Root, dirRelPath, finalFilename, stageRelPath)
	if err != nil {
		return "", err
	}
	if finalRelPath == stageRelPath {
		return stageRelPath, nil
	}
	stageAbsPath := filepath.Join(lib.Root, filepath.FromSlash(stageRelPath))
	finalAbsPath := filepath.Join(lib.Root, filepath.FromSlash(finalRelPath))
	if err := os.MkdirAll(filepath.Dir(finalAbsPath), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(stageAbsPath, finalAbsPath); err != nil {
		return "", err
	}
	return finalRelPath, nil
}

func buildEHBotStoredFilename(title string, ext string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled"
	}
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".zip"
	} else if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return safeEHBotArtifactFilename(title + ext)
}

func nextAvailableEHBotArtifactPath(rootDir string, dirRelPath string, filename string, currentRelPath string) (string, error) {
	candidateRelPath := shared.RelJoin(dirRelPath, filename)
	if candidateRelPath == currentRelPath {
		return candidateRelPath, nil
	}
	candidateAbsPath := filepath.Join(rootDir, filepath.FromSlash(candidateRelPath))
	if _, err := os.Stat(candidateAbsPath); err == nil {
		ext := path.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		for index := 2; ; index++ {
			candidateRelPath = shared.RelJoin(dirRelPath, fmt.Sprintf("%s (%d)%s", base, index, ext))
			if candidateRelPath == currentRelPath {
				return candidateRelPath, nil
			}
			candidateAbsPath = filepath.Join(rootDir, filepath.FromSlash(candidateRelPath))
			if _, err := os.Stat(candidateAbsPath); os.IsNotExist(err) {
				return candidateRelPath, nil
			} else if err != nil {
				return "", err
			}
		}
	} else if os.IsNotExist(err) {
		return candidateRelPath, nil
	} else {
		return "", err
	}
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func startEHBotHeartbeat(ctx context.Context, client *ehBotClient, jobID string, consumerID string, leaseSeconds int) func() {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	interval := time.Duration(leaseSeconds/2) * time.Second
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, _ = client.heartbeat(heartbeatCtx, jobID, consumerID, leaseSeconds)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func newEHBotClient(cfg configpkg.EHBotConfig) (*ehBotClient, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("ehbot base url is required")
	}
	return &ehBotClient{
		baseURL:    cfg.BaseURL,
		pullToken:  cfg.PullToken,
		consumerID: cfg.ConsumerID,
		httpClient: &http.Client{},
	}, nil
}

func (c *ehBotClient) listReadyJobs(ctx context.Context, limit int) ([]ehBotRemoteJob, error) {
	if limit <= 0 {
		limit = 1
	}
	endpoint := fmt.Sprintf("%s/api/v1/pull/jobs?status=ready&limit=%d", c.baseURL, limit)
	var payload struct {
		Items []ehBotRemoteJob `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}

func (c *ehBotClient) createDownloadJob(ctx context.Context, targetID string, input string) (ehBotRemoteJob, string, error) {
	payload := map[string]any{"input": strings.TrimSpace(input)}
	if strings.TrimSpace(targetID) != "" {
		payload["target_id"] = strings.TrimSpace(targetID)
	}
	endpoints := []string{"/api/v1/jobs", "/api/v1/pull/jobs"}
	var lastErr error
	for _, route := range endpoints {
		var out ehBotRemoteJob
		status, err := c.doJSONStatus(ctx, http.MethodPost, c.baseURL+route, payload, &out)
		if err == nil {
			return out, route, nil
		}
		lastErr = err
		if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
			continue
		}
		return ehBotRemoteJob{}, route, err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ehbot create endpoint is not available")
	}
	return ehBotRemoteJob{}, "", lastErr
}

func (c *ehBotClient) claim(ctx context.Context, jobID string, consumerID string, leaseSeconds int) (ehBotRemoteJob, error) {
	var out ehBotRemoteJob
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/pull/jobs/%s/claim", c.baseURL, url.PathEscape(jobID)), map[string]any{
		"consumer_id":   consumerID,
		"lease_seconds": leaseSeconds,
	}, &out)
	return out, err
}

func (c *ehBotClient) heartbeat(ctx context.Context, jobID string, consumerID string, leaseSeconds int) (ehBotRemoteJob, error) {
	var out ehBotRemoteJob
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/pull/jobs/%s/heartbeat", c.baseURL, url.PathEscape(jobID)), map[string]any{
		"consumer_id":   consumerID,
		"lease_seconds": leaseSeconds,
	}, &out)
	return out, err
}

func (c *ehBotClient) complete(ctx context.Context, jobID string, consumerID string, libraryID string, importedPath string, sha256sum string) error {
	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/pull/jobs/%s/complete", c.baseURL, url.PathEscape(jobID)), map[string]any{
		"consumer_id":   consumerID,
		"library_id":    libraryID,
		"imported_path": importedPath,
		"sha256":        sha256sum,
	}, nil)
}

func (c *ehBotClient) fail(ctx context.Context, jobID string, errorCode string, message string, retryable bool) (ehBotRemoteJob, error) {
	var out ehBotRemoteJob
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/pull/jobs/%s/fail", c.baseURL, url.PathEscape(jobID)), map[string]any{
		"error_code": errorCode,
		"message":    message,
		"retryable":  retryable,
	}, &out)
	return out, err
}

func (c *ehBotClient) downloadArtifact(ctx context.Context, jobID string) (*http.Response, error) {
	endpoint := fmt.Sprintf("%s/api/v1/pull/jobs/%s/artifact?consumer_id=%s", c.baseURL, url.PathEscape(jobID), url.QueryEscape(c.consumerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.pullToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.pullToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return nil, fmt.Errorf("artifact download failed: %s", strings.TrimSpace(string(body)))
}

func (c *ehBotClient) doJSON(ctx context.Context, method string, endpoint string, payload any, out any) error {
	_, err := c.doJSONStatus(ctx, method, endpoint, payload, out)
	return err
}

func (c *ehBotClient) doJSONStatus(ctx context.Context, method string, endpoint string, payload any, out any) (int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.pullToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.pullToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return resp.StatusCode, fmt.Errorf("ehbot request failed: %s", strings.TrimSpace(string(body)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

type EHBotLibraryOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type EHBotConfigView struct {
	Writable               bool                 `json:"writable"`
	ConfigPath             string               `json:"config_path,omitempty"`
	RewriteWarning         string               `json:"rewrite_warning,omitempty"`
	PullTokenConfigured    bool                 `json:"pull_token_configured"`
	Enabled                bool                 `json:"enabled"`
	BaseURL                string               `json:"base_url,omitempty"`
	ConsumerID             string               `json:"consumer_id,omitempty"`
	TargetID               string               `json:"target_id,omitempty"`
	TargetLibraryID        string               `json:"target_library_id,omitempty"`
	TargetSubdir           string               `json:"target_subdir,omitempty"`
	PollIntervalSeconds    int                  `json:"poll_interval_seconds"`
	LeaseSeconds           int                  `json:"lease_seconds"`
	DownloadTimeoutSeconds int                  `json:"download_timeout_seconds"`
	AutoRescan             bool                 `json:"auto_rescan"`
	MaxJobsPerPoll         int                  `json:"max_jobs_per_poll"`
	Libraries              []EHBotLibraryOption `json:"libraries,omitempty"`
}

type EHBotConfigUpdate struct {
	Enabled                bool   `json:"enabled"`
	BaseURL                string `json:"base_url"`
	PullToken              string `json:"pull_token,omitempty"`
	ClearPullToken         bool   `json:"clear_pull_token,omitempty"`
	ConsumerID             string `json:"consumer_id"`
	TargetID               string `json:"target_id"`
	TargetLibraryID        string `json:"target_library_id"`
	TargetSubdir           string `json:"target_subdir"`
	PollIntervalSeconds    int    `json:"poll_interval_seconds"`
	LeaseSeconds           int    `json:"lease_seconds"`
	DownloadTimeoutSeconds int    `json:"download_timeout_seconds"`
	AutoRescan             bool   `json:"auto_rescan"`
	MaxJobsPerPoll         int    `json:"max_jobs_per_poll"`
}

func (a *App) restartEHBotService() {
	a.stopEHBotService()
	a.startEHBotService()
}

func (a *App) EHBotConfigView() EHBotConfigView {
	cfg := normalizeEHBotConfig(a.cfg.EHBot)
	libraries := make([]EHBotLibraryOption, 0, len(a.cfg.Libraries))
	for _, lib := range a.cfg.Libraries {
		libraries = append(libraries, EHBotLibraryOption{ID: lib.ID, Name: lib.Name, Kind: lib.Kind})
	}
	return EHBotConfigView{
		Writable:               strings.TrimSpace(a.cfg.SourcePath) != "",
		ConfigPath:             a.cfg.SourcePath,
		RewriteWarning:         "Saving rewrites the current server config file.",
		PullTokenConfigured:    strings.TrimSpace(cfg.PullToken) != "",
		Enabled:                cfg.Enabled,
		BaseURL:                cfg.BaseURL,
		ConsumerID:             cfg.ConsumerID,
		TargetID:               cfg.TargetID,
		TargetLibraryID:        cfg.TargetLibraryID,
		TargetSubdir:           cfg.TargetSubdir,
		PollIntervalSeconds:    cfg.PollIntervalSeconds,
		LeaseSeconds:           cfg.LeaseSeconds,
		DownloadTimeoutSeconds: cfg.DownloadTimeoutSeconds,
		AutoRescan:             cfg.AutoRescan,
		MaxJobsPerPoll:         cfg.MaxJobsPerPoll,
		Libraries:              libraries,
	}
}

func (a *App) UpdateEHBotConfig(_ context.Context, update EHBotConfigUpdate) (EHBotConfigView, error) {
	path := strings.TrimSpace(a.cfg.SourcePath)
	if path == "" {
		return a.EHBotConfigView(), fmt.Errorf("config source path is not available")
	}
	previous := a.cfg.EHBot
	next := configpkg.EHBotConfig{
		Enabled:                update.Enabled,
		BaseURL:                strings.TrimSpace(update.BaseURL),
		PullToken:              strings.TrimSpace(update.PullToken),
		ConsumerID:             strings.TrimSpace(update.ConsumerID),
		TargetID:               strings.TrimSpace(update.TargetID),
		TargetLibraryID:        strings.TrimSpace(update.TargetLibraryID),
		TargetSubdir:           strings.TrimSpace(update.TargetSubdir),
		PollIntervalSeconds:    update.PollIntervalSeconds,
		LeaseSeconds:           update.LeaseSeconds,
		DownloadTimeoutSeconds: update.DownloadTimeoutSeconds,
		AutoRescan:             update.AutoRescan,
		MaxJobsPerPoll:         update.MaxJobsPerPoll,
	}
	if next.PullToken == "" && !update.ClearPullToken {
		next.PullToken = previous.PullToken
	}
	if update.ClearPullToken {
		next.PullToken = ""
	}
	a.cfg.EHBot = normalizeEHBotConfig(next)
	if err := configpkg.SaveConfig(path, a.cfg); err != nil {
		a.cfg.EHBot = previous
		return a.EHBotConfigView(), err
	}
	a.restartEHBotService()
	return a.EHBotConfigView(), nil
}
