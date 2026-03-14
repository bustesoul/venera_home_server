package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type JobRecord struct {
	ID          string          `json:"job_id"`
	Kind        string          `json:"kind"`
	Trigger     string          `json:"trigger,omitempty"`
	Status      string          `json:"status"`
	Summary     string          `json:"summary,omitempty"`
	LibraryID   string          `json:"library_id,omitempty"`
	Path        string          `json:"path,omitempty"`
	TargetID    string          `json:"target_id,omitempty"`
	RemoteJobID string          `json:"remote_job_id,omitempty"`
	Error       string          `json:"error,omitempty"`
	RequestedAt time.Time       `json:"requested_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type JobQuery struct {
	Kind      string
	Trigger   string
	Status    string
	LibraryID string
	Limit     int
	Offset    int
}

type JobListResult struct {
	Items  []JobRecord `json:"items"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

func (s *Store) UpsertJob(ctx context.Context, item JobRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	item.ID = strings.TrimSpace(item.ID)
	item.Kind = strings.TrimSpace(item.Kind)
	item.Trigger = strings.TrimSpace(item.Trigger)
	item.Status = strings.TrimSpace(item.Status)
	item.Summary = strings.TrimSpace(item.Summary)
	item.LibraryID = strings.TrimSpace(item.LibraryID)
	item.Path = strings.TrimSpace(item.Path)
	item.TargetID = strings.TrimSpace(item.TargetID)
	item.RemoteJobID = strings.TrimSpace(item.RemoteJobID)
	item.Error = strings.TrimSpace(item.Error)
	if item.ID == "" || item.Kind == "" || item.Status == "" {
		return errors.New("invalid job record")
	}
	if item.RequestedAt.IsZero() {
		item.RequestedAt = time.Now().UTC()
	} else {
		item.RequestedAt = item.RequestedAt.UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.RequestedAt
	} else {
		item.UpdatedAt = item.UpdatedAt.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO job_history(
	job_id, kind, trigger, status, summary, library_id, path, target_id,
	remote_job_id, error, requested_at, started_at, finished_at, updated_at,
	payload_json, result_json
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET
	kind = excluded.kind,
	trigger = excluded.trigger,
	status = excluded.status,
	summary = excluded.summary,
	library_id = excluded.library_id,
	path = excluded.path,
	target_id = excluded.target_id,
	remote_job_id = excluded.remote_job_id,
	error = excluded.error,
	requested_at = excluded.requested_at,
	started_at = excluded.started_at,
	finished_at = excluded.finished_at,
	updated_at = excluded.updated_at,
	payload_json = excluded.payload_json,
	result_json = excluded.result_json
`,
		item.ID,
		item.Kind,
		nullIfEmpty(item.Trigger),
		item.Status,
		nullIfEmpty(item.Summary),
		nullIfEmpty(item.LibraryID),
		nullIfEmpty(item.Path),
		nullIfEmpty(item.TargetID),
		nullIfEmpty(item.RemoteJobID),
		nullIfEmpty(item.Error),
		formatTime(item.RequestedAt),
		formatTimePtr(item.StartedAt),
		formatTimePtr(item.FinishedAt),
		formatTime(item.UpdatedAt),
		nullIfEmpty(strings.TrimSpace(string(item.Payload))),
		nullIfEmpty(strings.TrimSpace(string(item.Result))),
	)
	return err
}

func (s *Store) FailUnfinishedJobs(ctx context.Context, reason string, finishedAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "job terminated before completion"
	}
	finishedAt = finishedAt.UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE job_history
SET status = 'failed',
	error = ?,
	finished_at = ?,
	updated_at = ?
WHERE status IN ('queued', 'running')
`, reason, formatTime(finishedAt), formatTime(finishedAt))
	return err
}

func (s *Store) ListJobs(ctx context.Context, query JobQuery) ([]JobRecord, error) {
	result, err := s.ListJobsPage(ctx, query)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Store) ListJobsPage(ctx context.Context, query JobQuery) (JobListResult, error) {
	if s == nil || s.db == nil {
		return JobListResult{Limit: query.Limit, Offset: query.Offset}, nil
	}
	where, args := buildJobFilter(query)
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	countStmt := `SELECT COUNT(*) FROM job_history WHERE ` + strings.Join(where, ` AND `)
	var total int
	if err := s.db.QueryRowContext(ctx, countStmt, args...).Scan(&total); err != nil {
		return JobListResult{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, kind, trigger, status, summary, library_id, path, target_id,
	   remote_job_id, error, requested_at, started_at, finished_at, updated_at,
	   payload_json, result_json
FROM job_history
WHERE `+strings.Join(where, ` AND `)+`
ORDER BY updated_at DESC, job_id DESC
LIMIT ? OFFSET ?`, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return JobListResult{}, err
	}
	defer rows.Close()
	out := make([]JobRecord, 0)
	for rows.Next() {
		item, err := scanJob(rows)
		if err != nil {
			return JobListResult{}, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return JobListResult{}, err
	}
	return JobListResult{Items: out, Total: total, Limit: limit, Offset: offset}, nil
}

func buildJobFilter(query JobQuery) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if value := strings.TrimSpace(query.Kind); value != "" {
		where = append(where, "kind = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Trigger); value != "" {
		where = append(where, "trigger = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Status); value != "" {
		where = append(where, "status = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.LibraryID); value != "" {
		where = append(where, "library_id = ?")
		args = append(args, value)
	}
	return where, args
}

func scanJob(scanner rowScanner) (JobRecord, error) {
	var item JobRecord
	var trigger sql.NullString
	var summary sql.NullString
	var libraryID sql.NullString
	var path sql.NullString
	var targetID sql.NullString
	var remoteJobID sql.NullString
	var rawError sql.NullString
	var requestedAt sql.NullString
	var startedAt sql.NullString
	var finishedAt sql.NullString
	var updatedAt sql.NullString
	var payload sql.NullString
	var result sql.NullString
	err := scanner.Scan(
		&item.ID,
		&item.Kind,
		&trigger,
		&item.Status,
		&summary,
		&libraryID,
		&path,
		&targetID,
		&remoteJobID,
		&rawError,
		&requestedAt,
		&startedAt,
		&finishedAt,
		&updatedAt,
		&payload,
		&result,
	)
	if err != nil {
		return JobRecord{}, err
	}
	item.Trigger = trigger.String
	item.Summary = summary.String
	item.LibraryID = libraryID.String
	item.Path = path.String
	item.TargetID = targetID.String
	item.RemoteJobID = remoteJobID.String
	item.Error = rawError.String
	if parsed := parseTimePtr(requestedAt.String); parsed != nil {
		item.RequestedAt = *parsed
	}
	item.StartedAt = parseTimePtr(startedAt.String)
	item.FinishedAt = parseTimePtr(finishedAt.String)
	if parsed := parseTimePtr(updatedAt.String); parsed != nil {
		item.UpdatedAt = *parsed
	}
	item.Payload = rawJSON(payload.String)
	item.Result = rawJSON(result.String)
	return item, nil
}

func rawJSON(value string) json.RawMessage {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return json.RawMessage(value)
}

func (s *Store) DistinctJobKinds(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT kind
FROM job_history
ORDER BY kind
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	kinds := make([]string, 0)
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return kinds, nil
}

func (s *Store) PruneJobsByKind(ctx context.Context, kind string, keep int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return 0, nil
	}
	if keep <= 0 {
		result, err := s.db.ExecContext(ctx, `DELETE FROM job_history WHERE kind = ?`, kind)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected()
	}
	result, err := s.db.ExecContext(ctx, `
DELETE FROM job_history
WHERE kind = ? AND job_id NOT IN (
	SELECT job_id
	FROM job_history
	WHERE kind = ?
	ORDER BY updated_at DESC, job_id DESC
	LIMIT ?
)`, kind, kind, keep)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
