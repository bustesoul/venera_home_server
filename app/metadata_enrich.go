package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	exdbdryrunpkg "venera_home_server/exdbdryrun"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

type MetadataEnrichRequest struct {
	LibraryID    string               `json:"library_id,omitempty"`
	Locator      *metadatapkg.Locator `json:"locator,omitempty"`
	State        string               `json:"state,omitempty"`
	Limit        int                  `json:"limit,omitempty"`
	MinScore     float64              `json:"min_score,omitempty"`
	SourceID     string               `json:"source_id,omitempty"`
	Workers      int                  `json:"workers,omitempty"`
	IgnoreLocked bool                 `json:"ignore_locked,omitempty"`
	Trigger      string               `json:"trigger,omitempty"`
}

type MetadataSourceSummary struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	RelativePath string   `json:"relative_path,omitempty"`
	Path         string   `json:"path,omitempty"`
	ChosenTable  string   `json:"chosen_table,omitempty"`
	RowCount     int64    `json:"row_count,omitempty"`
	Score        int      `json:"score,omitempty"`
	Columns      []string `json:"columns,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type MetadataSourceBrowseResult struct {
	Source exdbSourceSummary          `json:"source"`
	Browse exdbdryrunpkg.BrowseResult `json:"browse"`
}

type MetadataRecordActionRequest struct {
	Locator  metadatapkg.Locator   `json:"locator,omitempty"`
	Locators []metadatapkg.Locator `json:"locators,omitempty"`
	Action   string                `json:"action"`
	SourceID string                `json:"source_id,omitempty"`
	MinScore float64               `json:"min_score,omitempty"`
	Workers  int                   `json:"workers,omitempty"`
}

type MetadataRecordActionResult struct {
	Action    string               `json:"action"`
	Processed int                  `json:"processed,omitempty"`
	Job       *MetadataRefreshJob  `json:"job,omitempty"`
	Jobs      []MetadataRefreshJob `json:"jobs,omitempty"`
	Record    *metadatapkg.Record  `json:"record,omitempty"`
	Records   []metadatapkg.Record `json:"records,omitempty"`
}

type exdbSourceHandle struct {
	Summary exdbSourceSummary
	Source  *exdbdryrunpkg.Source
}

type exdbSourceSummary = MetadataSourceSummary

func (a *App) StartMetadataEnrichment(ctx context.Context, req MetadataEnrichRequest) (MetadataRefreshJob, error) {
	if a.metadataStore == nil {
		return MetadataRefreshJob{}, nil
	}
	req = req.withDefaults()
	path := ""
	if req.Locator != nil {
		path = req.Locator.RootRef
	}
	now := time.Now().UTC()
	job := &MetadataRefreshJob{
		ID:          shared.SHAID("metadata-enrich", req.LibraryID, path, req.SourceID, req.State, now.Format(time.RFC3339Nano)),
		Kind:        "enrich",
		Trigger:     normalizeJobTrigger(req.Trigger, "manual"),
		Status:      "queued",
		LibraryID:   req.LibraryID,
		Path:        path,
		SourceID:    req.SourceID,
		State:       req.State,
		MinScore:    req.MinScore,
		Workers:     req.Workers,
		RequestedAt: now,
	}
	a.metadataJobsMu.Lock()
	if a.metadataJobs == nil {
		a.metadataJobs = map[string]*MetadataRefreshJob{}
	}
	a.metadataJobs[job.ID] = job
	a.metadataJobsMu.Unlock()
	a.syncMetadataJobHistory(job)
	go a.runMetadataEnrichmentJob(context.Background(), job, req)
	return *job, nil
}

func (a *App) MetadataSources(ctx context.Context) ([]MetadataSourceSummary, error) {
	return a.discoverExdbSources(ctx)
}

func (a *App) MetadataSourceBrowse(ctx context.Context, sourceID string, query exdbdryrunpkg.BrowseQuery) (MetadataSourceBrowseResult, error) {
	handles, err := a.openExdbSources(ctx, strings.TrimSpace(sourceID))
	if err != nil {
		return MetadataSourceBrowseResult{}, err
	}
	defer closeExdbSources(handles)
	if len(handles) == 0 {
		return MetadataSourceBrowseResult{}, fmt.Errorf("metadata source not found")
	}
	browse, err := handles[0].Source.Browse(ctx, query)
	if err != nil {
		return MetadataSourceBrowseResult{}, err
	}
	return MetadataSourceBrowseResult{Source: handles[0].Summary, Browse: browse}, nil
}

func normalizeMetadataActionLocators(req MetadataRecordActionRequest) ([]metadatapkg.Locator, error) {
	out := make([]metadatapkg.Locator, 0, 1+len(req.Locators))
	seen := map[string]struct{}{}
	appendLocator := func(locator metadatapkg.Locator) {
		if !locator.Valid() {
			return
		}
		key := locator.LibraryID + "\x00" + locator.RootType + "\x00" + locator.RootRef
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, locator)
	}
	appendLocator(req.Locator)
	for _, locator := range req.Locators {
		appendLocator(locator)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("invalid metadata locator")
	}
	return out, nil
}

func buildMetadataRecordActionResult(action string, locators []metadatapkg.Locator, records []metadatapkg.Record, jobs []MetadataRefreshJob) MetadataRecordActionResult {
	result := MetadataRecordActionResult{Action: action, Processed: len(locators)}
	if len(records) > 0 {
		result.Records = records
		firstRecord := records[0]
		result.Record = &firstRecord
	}
	if len(jobs) > 0 {
		result.Jobs = jobs
		firstJob := jobs[0]
		result.Job = &firstJob
	}
	return result
}

func (a *App) MetadataRecordAction(ctx context.Context, req MetadataRecordActionRequest) (MetadataRecordActionResult, error) {
	locators, err := normalizeMetadataActionLocators(req)
	if err != nil {
		return MetadataRecordActionResult{}, err
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case "lock", "unlock":
		records := make([]metadatapkg.Record, 0, len(locators))
		locked := action == "lock"
		for _, locator := range locators {
			if err := a.UpdateMetadata(ctx, locator, metadatapkg.Update{ManualLocked: locked, HasManualLocked: true}); err != nil {
				return MetadataRecordActionResult{}, err
			}
			record, err := a.metadataStore.GetByLocator(ctx, locator)
			if err != nil {
				return MetadataRecordActionResult{}, err
			}
			if record != nil {
				records = append(records, *record)
			}
		}
		return buildMetadataRecordActionResult(action, locators, records, nil), nil
	case "reset":
		now := time.Now().UTC()
		history := metadatapkg.JobRecord{
			ID:          shared.SHAID("metadata-reset", fmt.Sprintf("%d", len(locators)), now.Format(time.RFC3339Nano)),
			Kind:        "metadata.reset",
			Trigger:     "record_action",
			Status:      "running",
			Summary:     fmt.Sprintf("locators=%d", len(locators)),
			RequestedAt: now,
			StartedAt:   &now,
			UpdatedAt:   now,
			Payload:     rawJobJSON(map[string]any{"locators": locators}),
		}
		a.persistJobRecord(history)
		touchedLibraries := map[string]struct{}{}
		for _, locator := range locators {
			if err := a.ResetMetadata(ctx, locator); err != nil {
				finished := time.Now().UTC()
				history.Status = "failed"
				history.Error = err.Error()
				history.FinishedAt = &finished
				history.UpdatedAt = finished
				a.persistJobRecord(history)
				return MetadataRecordActionResult{}, err
			}
			touchedLibraries[locator.LibraryID] = struct{}{}
		}
		libraries := make([]string, 0, len(touchedLibraries))
		for libraryID := range touchedLibraries {
			libraries = append(libraries, libraryID)
		}
		sort.Strings(libraries)
		for _, libraryID := range libraries {
			if err := a.Rescan(ctx, libraryID); err != nil {
				finished := time.Now().UTC()
				history.Status = "failed"
				history.Error = err.Error()
				history.FinishedAt = &finished
				history.UpdatedAt = finished
				a.persistJobRecord(history)
				return MetadataRecordActionResult{}, err
			}
		}
		records := make([]metadatapkg.Record, 0, len(locators))
		for _, locator := range locators {
			record, err := a.metadataStore.GetByLocator(ctx, locator)
			if err != nil {
				return MetadataRecordActionResult{}, err
			}
			if record != nil {
				records = append(records, *record)
			}
		}
		finished := time.Now().UTC()
		history.Status = "done"
		history.FinishedAt = &finished
		history.UpdatedAt = finished
		history.Result = rawJobJSON(map[string]any{"processed": len(locators), "libraries": libraries})
		a.persistJobRecord(history)
		return buildMetadataRecordActionResult(action, locators, records, nil), nil
	case "enrich":
		jobs := make([]MetadataRefreshJob, 0, len(locators))
		for _, locator := range locators {
			job, err := a.StartMetadataEnrichment(ctx, MetadataEnrichRequest{
				LibraryID:    locator.LibraryID,
				Locator:      &locator,
				State:        "",
				Limit:        1,
				MinScore:     req.MinScore,
				SourceID:     req.SourceID,
				Workers:      req.Workers,
				IgnoreLocked: true,
				Trigger:      "record_action",
			})
			if err != nil {
				return MetadataRecordActionResult{}, err
			}
			jobs = append(jobs, job)
		}
		return buildMetadataRecordActionResult(action, locators, nil, jobs), nil
	default:
		return MetadataRecordActionResult{}, fmt.Errorf("unsupported action %q", req.Action)
	}
}

func (a *App) runMetadataEnrichmentJob(ctx context.Context, job *MetadataRefreshJob, req MetadataEnrichRequest) {
	started := time.Now().UTC()
	a.metadataJobsMu.Lock()
	job.Status = "running"
	job.StartedAt = &started
	a.metadataJobsMu.Unlock()
	a.syncMetadataJobHistory(job)

	handles, err := a.openExdbSources(ctx, req.SourceID)
	if err != nil {
		a.finishMetadataJob(job, err)
		return
	}
	defer closeExdbSources(handles)

	records, err := a.metadataEnrichmentTargets(ctx, req)
	if err != nil {
		a.finishMetadataJob(job, err)
		return
	}

	sourceNames := make([]string, 0, len(handles))
	for _, handle := range handles {
		sourceNames = append(sourceNames, handle.Summary.Name)
	}
	a.metadataJobsMu.Lock()
	job.Examined = len(records)
	job.Sources = sourceNames
	a.metadataJobsMu.Unlock()

	if len(records) == 0 {
		a.finishMetadataJob(job, nil)
		return
	}

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		matched   bool
		updated   bool
		skipped   bool
		libraryID string
		err       error
	}
	workCh := make(chan metadatapkg.Record)
	resultCh := make(chan outcome, len(records))
	var wg sync.WaitGroup
	for workerIndex := 0; workerIndex < req.Workers; workerIndex++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for record := range workCh {
				if jobCtx.Err() != nil {
					return
				}
				matched, updated, skipped, err := a.enrichMetadataRecord(jobCtx, handles, record, req)
				resultCh <- outcome{matched: matched, updated: updated, skipped: skipped, libraryID: record.LibraryID, err: err}
				if err != nil {
					cancel()
					return
				}
			}
		}()
	}
	go func() {
		for _, record := range records {
			if jobCtx.Err() != nil {
				break
			}
			workCh <- record
		}
		close(workCh)
		wg.Wait()
		close(resultCh)
	}()

	updatedLibraries := map[string]bool{}
	var firstErr error
	for item := range resultCh {
		a.metadataJobsMu.Lock()
		job.Processed++
		if item.matched {
			job.Matched++
		}
		if item.updated {
			job.Updated++
		}
		if item.skipped {
			job.Skipped++
		}
		if !item.matched && !item.skipped && item.err == nil {
			job.Unmatched++
		}
		a.metadataJobsMu.Unlock()
		if item.updated && item.libraryID != "" {
			updatedLibraries[item.libraryID] = true
		}
		if item.err != nil && firstErr == nil {
			firstErr = item.err
		}
	}
	if firstErr != nil {
		a.finishMetadataJob(job, firstErr)
		return
	}
	if job.Updated > 0 {
		rescanLibrary := req.LibraryID
		if req.Locator != nil && req.Locator.LibraryID != "" {
			rescanLibrary = req.Locator.LibraryID
		}
		if rescanLibrary == "" && len(updatedLibraries) == 1 {
			for libraryID := range updatedLibraries {
				rescanLibrary = libraryID
			}
		}
		if len(updatedLibraries) > 1 {
			rescanLibrary = ""
		}
		if err := a.Rescan(ctx, rescanLibrary); err != nil {
			a.finishMetadataJob(job, err)
			return
		}
	}
	a.finishMetadataJob(job, nil)
}

func (a *App) finishMetadataJob(job *MetadataRefreshJob, err error) {
	finished := time.Now().UTC()
	a.metadataJobsMu.Lock()
	defer a.metadataJobsMu.Unlock()
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
	} else {
		job.Status = "done"
	}
	job.FinishedAt = &finished
	trimMetadataJobsLocked(a.metadataJobs)
	a.syncMetadataJobHistory(job)
}

func (a *App) enrichMetadataRecord(ctx context.Context, handles []exdbSourceHandle, record metadatapkg.Record, req MetadataEnrichRequest) (bool, bool, bool, error) {
	if record.ManualLocked && !req.IgnoreLocked {
		return false, false, true, nil
	}
	matched, summary, err := matchRecordFromSources(ctx, handles, record, req.MinScore)
	if err != nil {
		return false, false, false, err
	}
	if matched == nil {
		return false, false, false, nil
	}
	if err := a.UpdateMetadata(ctx, recordLocator(record), buildMetadataUpdateFromCandidate(summary, *matched)); err != nil {
		return true, false, false, err
	}
	return true, true, false, nil
}

func matchRecordFromSources(ctx context.Context, handles []exdbSourceHandle, record metadatapkg.Record, minScore float64) (*exdbdryrunpkg.Candidate, exdbSourceSummary, error) {
	var best *exdbdryrunpkg.Candidate
	var bestSource exdbSourceSummary
	for _, handle := range handles {
		candidate, _, err := handle.Source.MatchRecord(ctx, record, minScore)
		if err != nil {
			return nil, exdbSourceSummary{}, err
		}
		if candidate == nil {
			continue
		}
		if best == nil || candidate.Score > best.Score || (candidate.Score == best.Score && handle.Summary.Name < bestSource.Name) {
			copy := *candidate
			best = &copy
			bestSource = handle.Summary
		}
	}
	return best, bestSource, nil
}

func buildMetadataUpdateFromCandidate(source exdbSourceSummary, candidate exdbdryrunpkg.Candidate) metadatapkg.Update {
	now := time.Now().UTC()
	update := metadatapkg.Update{
		Title:          preferredCandidateTitle(candidate),
		TitleJPN:       strings.TrimSpace(candidate.TitleJPN),
		Artists:        parseExternalStringList(candidate.Artists),
		Tags:           parseExternalStringList(candidate.Tags),
		Language:       strings.TrimSpace(candidate.Language),
		Category:       strings.TrimSpace(candidate.Category),
		Source:         "exdb:" + source.Name,
		SourceID:       firstNonEmpty(strings.TrimSpace(candidate.GID), strings.TrimSpace(candidate.RowID)),
		SourceToken:    strings.TrimSpace(candidate.Token),
		SourceURL:      preferredCandidateURL(candidate),
		MatchKind:      strings.TrimSpace(candidate.Method),
		Confidence:     candidate.Score,
		HasConfidence:  true,
		CoverSourceURL: strings.TrimSpace(candidate.CoverURL),
		FetchedAt:      &now,
	}
	if rating, ok := parseCandidateRating(candidate.Rating); ok {
		update.Rating = rating
		update.HasRating = true
	}
	return update
}

func preferredCandidateTitle(candidate exdbdryrunpkg.Candidate) string {
	return firstNonEmpty(strings.TrimSpace(candidate.TitleJPN), strings.TrimSpace(candidate.Title))
}

func preferredCandidateURL(candidate exdbdryrunpkg.Candidate) string {
	if value := strings.TrimSpace(candidate.SourceURL); value != "" {
		return value
	}
	gid := strings.TrimSpace(candidate.GID)
	token := strings.TrimSpace(candidate.Token)
	if gid != "" && token != "" {
		return fmt.Sprintf("https://e-hentai.org/g/%s/%s/", gid, token)
	}
	return ""
}

func parseCandidateRating(raw string) (float64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseExternalStringList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	candidates := []string{trimmed}
	if strings.Contains(trimmed, `'`) {
		candidates = append(candidates, strings.ReplaceAll(trimmed, `'`, "\""))
	}
	for _, item := range candidates {
		var values []string
		if json.Unmarshal([]byte(item), &values) == nil {
			return shared.UniqueStrings(cleanExternalList(values))
		}
		var mixed []any
		if json.Unmarshal([]byte(item), &mixed) == nil {
			values = make([]string, 0, len(mixed))
			for _, value := range mixed {
				values = append(values, strings.TrimSpace(fmt.Sprint(value)))
			}
			return shared.UniqueStrings(cleanExternalList(values))
		}
	}
	trimmed = strings.Trim(trimmed, "[]")
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		switch r {
		case ',', ';', '|':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		parts = []string{trimmed}
	}
	return shared.UniqueStrings(cleanExternalList(parts))
}

func cleanExternalList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.Trim(strings.TrimSpace(value), `"'`)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}
	return cleaned
}

func recordLocator(record metadatapkg.Record) metadatapkg.Locator {
	return metadatapkg.Locator{LibraryID: record.LibraryID, RootType: record.RootType, RootRef: record.RootRef}
}

func (req MetadataEnrichRequest) withDefaults() MetadataEnrichRequest {
	req.LibraryID = strings.TrimSpace(req.LibraryID)
	req.SourceID = strings.TrimSpace(req.SourceID)
	req.State = strings.ToLower(strings.TrimSpace(req.State))
	if req.Locator != nil && req.Locator.Valid() {
		req.LibraryID = req.Locator.LibraryID
	}
	if req.State == "" && req.Locator == nil {
		req.State = "empty"
	}
	if req.Limit <= 0 {
		req.Limit = 200
	}
	if req.MinScore <= 0 {
		req.MinScore = 0.72
	}
	if req.Workers <= 0 {
		req.Workers = 3
	}
	if req.Workers > 4 {
		req.Workers = 4
	}
	return req
}

func (a *App) metadataEnrichmentTargets(ctx context.Context, req MetadataEnrichRequest) ([]metadatapkg.Record, error) {
	if req.Locator != nil && req.Locator.Valid() {
		record, err := a.metadataStore.GetByLocator(ctx, *req.Locator)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, nil
		}
		return []metadatapkg.Record{*record}, nil
	}
	page, err := a.MetadataRecordsPage(ctx, metadatapkg.ListQuery{
		LibraryID: req.LibraryID,
		State:     req.State,
		Limit:     req.Limit,
		Offset:    0,
	})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (a *App) openExdbSources(ctx context.Context, requestedID string) ([]exdbSourceHandle, error) {
	summaries, err := a.discoverExdbSources(ctx)
	if err != nil {
		return nil, err
	}
	handles := make([]exdbSourceHandle, 0, len(summaries))
	for _, summary := range summaries {
		if requestedID != "" && summary.ID != requestedID {
			continue
		}
		if summary.Error != "" {
			if requestedID == summary.ID {
				return nil, fmt.Errorf(summary.Error)
			}
			continue
		}
		source, err := exdbdryrunpkg.OpenSource(ctx, summary.Path, "")
		if err != nil {
			if requestedID == summary.ID {
				return nil, err
			}
			continue
		}
		handles = append(handles, exdbSourceHandle{Summary: summary, Source: source})
	}
	if requestedID != "" && len(handles) == 0 {
		return nil, fmt.Errorf("metadata source %q not found", requestedID)
	}
	if requestedID == "" && len(handles) == 0 {
		return nil, fmt.Errorf("no usable external metadata source found in %s", a.externalDBDir())
	}
	return handles, nil
}

func closeExdbSources(handles []exdbSourceHandle) {
	for _, handle := range handles {
		if handle.Source != nil {
			_ = handle.Source.Close()
		}
	}
}

func (a *App) discoverExdbSources(ctx context.Context) ([]MetadataSourceSummary, error) {
	dir := a.externalDBDir()
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	summaries := []MetadataSourceSummary{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d == nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		summary := MetadataSourceSummary{
			ID:           shared.SHAID("externaldb", rel),
			Kind:         "exdb",
			Name:         filepath.Base(path),
			RelativePath: rel,
			Path:         path,
		}
		source, err := exdbdryrunpkg.OpenSource(ctx, path, "")
		if err != nil {
			summary.Error = err.Error()
			summaries = append(summaries, summary)
			return nil
		}
		schema := source.Schema()
		_ = source.Close()
		summary.ChosenTable = schema.ChosenTable
		for _, table := range schema.Tables {
			if table.Name != schema.ChosenTable {
				continue
			}
			summary.RowCount = table.RowCount
			summary.Score = table.Score
			summary.Columns = append([]string(nil), table.Columns...)
			break
		}
		summaries = append(summaries, summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if (summaries[i].Error == "") != (summaries[j].Error == "") {
			return summaries[i].Error == ""
		}
		return summaries[i].RelativePath < summaries[j].RelativePath
	})
	return summaries, nil
}

func (a *App) externalDBDir() string {
	if a == nil || a.cfg == nil {
		return ""
	}
	return filepath.Join(a.cfg.Server.DataDir, "externaldb")
}
