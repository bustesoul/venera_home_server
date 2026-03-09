package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Locator struct {
	LibraryID string `json:"library_id"`
	RootType  string `json:"root_type"`
	RootRef   string `json:"root_ref"`
}

func (l Locator) Valid() bool {
	return strings.TrimSpace(l.LibraryID) != "" && strings.TrimSpace(l.RootType) != "" && strings.TrimSpace(l.RootRef) != ""
}

type Hint struct {
	EHGalleryID    string   `json:"eh_gallery_id,omitempty"`
	EHToken        string   `json:"eh_token,omitempty"`
	PixivIllustIDs []string `json:"pixiv_illust_ids,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`
}

type Record struct {
	ID                 int64      `json:"id"`
	LibraryID          string     `json:"library_id"`
	RootType           string     `json:"root_type"`
	RootRef            string     `json:"root_ref"`
	FolderPath         string     `json:"folder_path"`
	ContentFingerprint string     `json:"content_fingerprint,omitempty"`
	Title              string     `json:"title,omitempty"`
	TitleJPN           string     `json:"title_jpn,omitempty"`
	Subtitle           string     `json:"subtitle,omitempty"`
	Description        string     `json:"description,omitempty"`
	Artists            []string   `json:"artists,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
	Language           string     `json:"language,omitempty"`
	Rating             float64    `json:"rating,omitempty"`
	HasRating          bool       `json:"-"`
	Category           string     `json:"category,omitempty"`
	Source             string     `json:"source,omitempty"`
	SourceID           string     `json:"source_id,omitempty"`
	SourceToken        string     `json:"source_token,omitempty"`
	SourceURL          string     `json:"source_url,omitempty"`
	MatchKind          string     `json:"match_kind,omitempty"`
	Confidence         float64    `json:"confidence,omitempty"`
	HasConfidence      bool       `json:"-"`
	ManualLocked       bool       `json:"manual_locked,omitempty"`
	CoverSourceURL     string     `json:"cover_source_url,omitempty"`
	CoverBlobRelpath   string     `json:"cover_blob_relpath,omitempty"`
	Hint               Hint       `json:"hint,omitempty"`
	ExtraJSON          string     `json:"extra_json,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	FetchedAt          *time.Time `json:"fetched_at,omitempty"`
	StaleAfter         *time.Time `json:"stale_after,omitempty"`
	LastSeenAt         *time.Time `json:"last_seen_at,omitempty"`
	MissingSince       *time.Time `json:"missing_since,omitempty"`
}

func (r Record) IsEmptyMetadata() bool {
	return strings.TrimSpace(r.Title) == "" &&
		strings.TrimSpace(r.TitleJPN) == "" &&
		strings.TrimSpace(r.Subtitle) == "" &&
		strings.TrimSpace(r.Description) == "" &&
		len(r.Artists) == 0 &&
		len(r.Tags) == 0 &&
		strings.TrimSpace(r.Language) == "" &&
		!r.HasRating &&
		strings.TrimSpace(r.Category) == "" &&
		strings.TrimSpace(r.Source) == "" &&
		strings.TrimSpace(r.SourceID) == "" &&
		strings.TrimSpace(r.SourceURL) == "" &&
		strings.TrimSpace(r.CoverSourceURL) == "" &&
		strings.TrimSpace(r.CoverBlobRelpath) == "" &&
		strings.TrimSpace(r.ExtraJSON) == ""
}

type ScanInput struct {
	Locator            Locator
	FolderPath         string
	ContentFingerprint string
	Hint               Hint
}

type Update struct {
	Title            string
	TitleJPN         string
	Subtitle         string
	Description      string
	Artists          []string
	Tags             []string
	Language         string
	Rating           float64
	HasRating        bool
	Category         string
	Source           string
	SourceID         string
	SourceToken      string
	SourceURL        string
	MatchKind        string
	Confidence       float64
	HasConfidence    bool
	ManualLocked     bool
	HasManualLocked  bool
	CoverSourceURL   string
	CoverBlobRelpath string
	ExtraJSON        string
	LastError        string
	FetchedAt        *time.Time
	StaleAfter       *time.Time
}

type ListQuery struct {
	State         string
	LibraryID     string
	Path          string
	Search        string
	Limit         int
	Offset        int
	IncludeLocked bool
}

type ListResult struct {
	Items  []Record `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

type CleanupRequest struct {
	LibraryID     string `json:"library_id,omitempty"`
	OlderThanDays int    `json:"older_than_days,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
}

type CleanupResult struct {
	Matched int  `json:"matched"`
	Deleted int  `json:"deleted"`
	DryRun  bool `json:"dry_run"`
}

type Store struct {
	db   *sql.DB
	path string
}

func OpenStore(dataDir string, configuredPath string) (*Store, error) {
	path := strings.TrimSpace(configuredPath)
	if path == "" {
		path = filepath.Join(dataDir, "metadata.db")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(dataDir, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func initSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS manga_metadata (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id TEXT NOT NULL,
    root_type TEXT NOT NULL,
    root_ref TEXT NOT NULL,
    folder_path TEXT NOT NULL,
    content_fingerprint TEXT,
    title TEXT,
    title_jpn TEXT,
    subtitle TEXT,
    description TEXT,
    artists_json TEXT,
    tags_json TEXT,
    language TEXT,
    rating REAL,
    category TEXT,
    source TEXT,
    source_id TEXT,
    source_token TEXT,
    source_url TEXT,
    match_kind TEXT,
    confidence REAL,
    manual_locked INTEGER NOT NULL DEFAULT 0,
    cover_source_url TEXT,
    cover_blob_relpath TEXT,
    hint_json TEXT,
    extra_json TEXT,
    last_error TEXT,
    fetched_at TEXT,
    stale_after TEXT,
    last_seen_at TEXT NOT NULL,
    missing_since TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_manga_metadata_locator ON manga_metadata(library_id, root_type, root_ref);
CREATE INDEX IF NOT EXISTS idx_manga_metadata_folder_path ON manga_metadata(folder_path);
CREATE INDEX IF NOT EXISTS idx_manga_metadata_content_fp ON manga_metadata(content_fingerprint);
CREATE INDEX IF NOT EXISTS idx_manga_metadata_missing_since ON manga_metadata(missing_since);

CREATE TABLE IF NOT EXISTS job_history (
    job_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    trigger TEXT,
    status TEXT NOT NULL,
    summary TEXT,
    library_id TEXT,
    path TEXT,
    target_id TEXT,
    remote_job_id TEXT,
    error TEXT,
    requested_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL,
    payload_json TEXT,
    result_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_job_history_kind ON job_history(kind);
CREATE INDEX IF NOT EXISTS idx_job_history_status ON job_history(status);
CREATE INDEX IF NOT EXISTS idx_job_history_updated_at ON job_history(updated_at DESC, job_id DESC);
`
	_, err := db.Exec(schema)
	return err
}

func (s *Store) UpsertScanned(ctx context.Context, input ScanInput, seenAt time.Time) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if !input.Locator.Valid() {
		return nil, errors.New("invalid metadata locator")
	}
	seenAt = seenAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	hintJSON, err := encodeJSON(input.Hint)
	if err != nil {
		return nil, err
	}

	existing, err := s.getByLocatorTx(ctx, tx, input.Locator)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		_, err = tx.ExecContext(ctx, `
UPDATE manga_metadata
SET folder_path = ?, content_fingerprint = ?, hint_json = ?, last_seen_at = ?, missing_since = NULL
WHERE id = ?
`, input.FolderPath, nullIfEmpty(input.ContentFingerprint), nullIfEmpty(hintJSON), formatTime(seenAt), existing.ID)
		if err != nil {
			return nil, err
		}
		existing.FolderPath = input.FolderPath
		existing.ContentFingerprint = input.ContentFingerprint
		existing.Hint = input.Hint
		existing.LastSeenAt = ptrTime(seenAt)
		existing.MissingSince = nil
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	}

	rebound, err := s.findRebindCandidateTx(ctx, tx, input.Locator.LibraryID, input.ContentFingerprint, seenAt)
	if err != nil {
		return nil, err
	}
	if rebound != nil {
		_, err = tx.ExecContext(ctx, `
UPDATE manga_metadata
SET root_type = ?, root_ref = ?, folder_path = ?, content_fingerprint = ?, hint_json = ?, last_seen_at = ?, missing_since = NULL
WHERE id = ?
`, input.Locator.RootType, input.Locator.RootRef, input.FolderPath, nullIfEmpty(input.ContentFingerprint), nullIfEmpty(hintJSON), formatTime(seenAt), rebound.ID)
		if err != nil {
			return nil, err
		}
		rebound.RootType = input.Locator.RootType
		rebound.RootRef = input.Locator.RootRef
		rebound.FolderPath = input.FolderPath
		rebound.ContentFingerprint = input.ContentFingerprint
		rebound.Hint = input.Hint
		rebound.LastSeenAt = ptrTime(seenAt)
		rebound.MissingSince = nil
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return rebound, nil
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO manga_metadata(
    library_id, root_type, root_ref, folder_path, content_fingerprint, hint_json, last_seen_at
) VALUES(?, ?, ?, ?, ?, ?, ?)
`, input.Locator.LibraryID, input.Locator.RootType, input.Locator.RootRef, input.FolderPath, nullIfEmpty(input.ContentFingerprint), nullIfEmpty(hintJSON), formatTime(seenAt))
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	created, err := s.getByIDTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) FinalizeLibraryScan(ctx context.Context, libraryID string, seenAt time.Time) error {
	if s == nil || s.db == nil || strings.TrimSpace(libraryID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE manga_metadata
SET missing_since = ?
WHERE library_id = ? AND last_seen_at < ? AND missing_since IS NULL
`, formatTime(seenAt.UTC()), libraryID, formatTime(seenAt.UTC()))
	return err
}

func (s *Store) GetByLocator(ctx context.Context, locator Locator) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	return s.getByLocatorCtx(ctx, s.db, locator)
}

func (s *Store) ApplyUpdate(ctx context.Context, locator Locator, update Update) error {
	if s == nil || s.db == nil {
		return nil
	}
	if !locator.Valid() {
		return errors.New("invalid metadata locator")
	}
	artistsJSON, err := encodeJSON(update.Artists)
	if err != nil {
		return err
	}
	tagsJSON, err := encodeJSON(update.Tags)
	if err != nil {
		return err
	}
	args := []any{
		nullIfEmpty(update.Title),
		nullIfEmpty(update.TitleJPN),
		nullIfEmpty(update.Subtitle),
		nullIfEmpty(update.Description),
		nullIfEmpty(artistsJSON),
		nullIfEmpty(tagsJSON),
		nullIfEmpty(update.Language),
		nil,
		nullIfEmpty(update.Category),
		nullIfEmpty(update.Source),
		nullIfEmpty(update.SourceID),
		nullIfEmpty(update.SourceToken),
		nullIfEmpty(update.SourceURL),
		nullIfEmpty(update.MatchKind),
		nil,
		nil,
		nullIfEmpty(update.CoverSourceURL),
		nullIfEmpty(update.CoverBlobRelpath),
		nullIfEmpty(update.ExtraJSON),
		nullIfEmpty(update.LastError),
		formatTimePtr(update.FetchedAt),
		formatTimePtr(update.StaleAfter),
		locator.LibraryID,
		locator.RootType,
		locator.RootRef,
	}
	if update.HasRating {
		args[7] = update.Rating
	}
	if update.HasConfidence {
		args[14] = update.Confidence
	}
	if update.HasManualLocked {
		if update.ManualLocked {
			args[15] = 1
		} else {
			args[15] = 0
		}
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE manga_metadata
SET title = COALESCE(?, title), title_jpn = COALESCE(?, title_jpn), subtitle = COALESCE(?, subtitle),
    description = COALESCE(?, description), artists_json = COALESCE(?, artists_json), tags_json = COALESCE(?, tags_json),
    language = COALESCE(?, language), rating = COALESCE(?, rating), category = COALESCE(?, category),
    source = COALESCE(?, source), source_id = COALESCE(?, source_id), source_token = COALESCE(?, source_token),
    source_url = COALESCE(?, source_url), match_kind = COALESCE(?, match_kind), confidence = COALESCE(?, confidence),
    manual_locked = COALESCE(?, manual_locked), cover_source_url = COALESCE(?, cover_source_url),
    cover_blob_relpath = COALESCE(?, cover_blob_relpath), extra_json = COALESCE(?, extra_json),
    last_error = COALESCE(?, last_error), fetched_at = COALESCE(?, fetched_at), stale_after = COALESCE(?, stale_after)
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ResetMetadata(ctx context.Context, locator Locator) error {
	if s == nil || s.db == nil {
		return nil
	}
	if !locator.Valid() {
		return errors.New("invalid metadata locator")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE manga_metadata
SET title = NULL, title_jpn = NULL, subtitle = NULL, description = NULL,
    artists_json = NULL, tags_json = NULL, language = NULL, rating = NULL,
    category = NULL, source = NULL, source_id = NULL, source_token = NULL,
    source_url = NULL, match_kind = NULL, confidence = NULL,
    cover_source_url = NULL, cover_blob_relpath = NULL, extra_json = NULL,
    last_error = NULL, fetched_at = NULL, stale_after = NULL
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, locator.LibraryID, locator.RootType, locator.RootRef)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListRecords(ctx context.Context, query ListQuery) ([]Record, error) {
	result, err := s.ListRecordsPage(ctx, query)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Store) ListRecordsPage(ctx context.Context, query ListQuery) (ListResult, error) {
	if s == nil || s.db == nil {
		return ListResult{Limit: query.Limit, Offset: query.Offset}, nil
	}
	where, args := buildListRecordsFilter(query)
	limit := query.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	countStmt := `SELECT COUNT(*) FROM manga_metadata WHERE ` + strings.Join(where, ` AND `)
	var total int
	if err := s.db.QueryRowContext(ctx, countStmt, args...).Scan(&total); err != nil {
		return ListResult{}, err
	}
	stmt := `
SELECT id, library_id, root_type, root_ref, folder_path, content_fingerprint,
       title, title_jpn, subtitle, description, artists_json, tags_json,
       language, rating, category, source, source_id, source_token, source_url,
       match_kind, confidence, manual_locked, cover_source_url, cover_blob_relpath,
       hint_json, extra_json, last_error, fetched_at, stale_after, last_seen_at, missing_since
FROM manga_metadata
WHERE ` + strings.Join(where, ` AND `) + `
ORDER BY COALESCE(last_seen_at, '') DESC, id DESC
LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, stmt, queryArgs...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return ListResult{}, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: out, Total: total, Limit: limit, Offset: offset}, nil
}

func buildListRecordsFilter(query ListQuery) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if strings.TrimSpace(query.LibraryID) != "" {
		where = append(where, "library_id = ?")
		args = append(args, strings.TrimSpace(query.LibraryID))
	}
	if path := strings.TrimSpace(query.Path); path != "" {
		where = append(where, "(root_ref LIKE ? OR folder_path LIKE ?)")
		args = append(args, path+"%", path+"%")
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		pattern := "%" + search + "%"
		where = append(where, `(root_ref LIKE ? OR folder_path LIKE ? OR COALESCE(title, '') LIKE ? OR COALESCE(title_jpn, '') LIKE ? OR COALESCE(subtitle, '') LIKE ? OR COALESCE(description, '') LIKE ? OR COALESCE(artists_json, '') LIKE ? OR COALESCE(tags_json, '') LIKE ? OR COALESCE(category, '') LIKE ? OR COALESCE(source, '') LIKE ? OR COALESCE(source_id, '') LIKE ? OR COALESCE(last_error, '') LIKE ?)`)
		for i := 0; i < 12; i++ {
			args = append(args, pattern)
		}
	}
	now := formatTime(time.Now().UTC())
	switch strings.ToLower(strings.TrimSpace(query.State)) {
	case "locked":
		where = append(where, "manual_locked <> 0")
	case "empty":
		if !query.IncludeLocked {
			where = append(where, "manual_locked = 0")
		}
		where = append(where,
			"missing_since IS NULL",
			"COALESCE(last_error, '') = ''",
			"(stale_after IS NULL OR stale_after = '' OR stale_after > ?)",
			emptyStateWhere(),
		)
		args = append(args, now)
	case "missing":
		if !query.IncludeLocked {
			where = append(where, "manual_locked = 0")
		}
		where = append(where, "missing_since IS NOT NULL")
	case "error":
		if !query.IncludeLocked {
			where = append(where, "manual_locked = 0")
		}
		where = append(where, "missing_since IS NULL", "COALESCE(last_error, '') <> ''")
	case "stale":
		if !query.IncludeLocked {
			where = append(where, "manual_locked = 0")
		}
		where = append(where,
			"missing_since IS NULL",
			"COALESCE(last_error, '') = ''",
			"stale_after IS NOT NULL AND stale_after <> '' AND stale_after <= ?",
		)
		args = append(args, now)
	case "ready":
		if !query.IncludeLocked {
			where = append(where, "manual_locked = 0")
		}
		where = append(where,
			"missing_since IS NULL",
			"COALESCE(last_error, '') = ''",
			"(stale_after IS NULL OR stale_after = '' OR stale_after > ?)",
			"NOT ("+emptyStateWhere()+")",
		)
		args = append(args, now)
	}
	return where, args
}

func (s *Store) CleanupMissing(ctx context.Context, req CleanupRequest) (CleanupResult, error) {
	if s == nil || s.db == nil {
		return CleanupResult{DryRun: req.DryRun}, nil
	}
	where := []string{"missing_since IS NOT NULL"}
	args := []any{}
	if strings.TrimSpace(req.LibraryID) != "" {
		where = append(where, "library_id = ?")
		args = append(args, strings.TrimSpace(req.LibraryID))
	}
	if req.OlderThanDays > 0 {
		threshold := time.Now().UTC().Add(-time.Duration(req.OlderThanDays) * 24 * time.Hour)
		where = append(where, "missing_since <= ?")
		args = append(args, formatTime(threshold))
	}
	countQuery := `SELECT COUNT(*) FROM manga_metadata WHERE ` + strings.Join(where, ` AND `)
	var matched int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&matched); err != nil {
		return CleanupResult{DryRun: req.DryRun}, err
	}
	result := CleanupResult{Matched: matched, DryRun: req.DryRun}
	if req.DryRun || matched == 0 {
		return result, nil
	}
	deleteQuery := `DELETE FROM manga_metadata WHERE ` + strings.Join(where, ` AND `)
	deleted, err := s.db.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		return result, err
	}
	affected, err := deleted.RowsAffected()
	if err != nil {
		return result, err
	}
	result.Deleted = int(affected)
	return result, nil
}

func (s *Store) getByLocatorCtx(ctx context.Context, querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, locator Locator) (*Record, error) {
	if !locator.Valid() {
		return nil, nil
	}
	row := querier.QueryRowContext(ctx, `
SELECT id, library_id, root_type, root_ref, folder_path, content_fingerprint,
       title, title_jpn, subtitle, description, artists_json, tags_json,
       language, rating, category, source, source_id, source_token, source_url,
       match_kind, confidence, manual_locked, cover_source_url, cover_blob_relpath,
       hint_json, extra_json, last_error, fetched_at, stale_after, last_seen_at, missing_since
FROM manga_metadata
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, locator.LibraryID, locator.RootType, locator.RootRef)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) getByLocatorTx(ctx context.Context, tx *sql.Tx, locator Locator) (*Record, error) {
	if !locator.Valid() {
		return nil, nil
	}
	row := tx.QueryRowContext(ctx, `
SELECT id, library_id, root_type, root_ref, folder_path, content_fingerprint,
       title, title_jpn, subtitle, description, artists_json, tags_json,
       language, rating, category, source, source_id, source_token, source_url,
       match_kind, confidence, manual_locked, cover_source_url, cover_blob_relpath,
       hint_json, extra_json, last_error, fetched_at, stale_after, last_seen_at, missing_since
FROM manga_metadata
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, locator.LibraryID, locator.RootType, locator.RootRef)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) getByIDTx(ctx context.Context, tx *sql.Tx, id int64) (*Record, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, library_id, root_type, root_ref, folder_path, content_fingerprint,
       title, title_jpn, subtitle, description, artists_json, tags_json,
       language, rating, category, source, source_id, source_token, source_url,
       match_kind, confidence, manual_locked, cover_source_url, cover_blob_relpath,
       hint_json, extra_json, last_error, fetched_at, stale_after, last_seen_at, missing_since
FROM manga_metadata
WHERE id = ?
`, id)
	rec, err := scanRecord(row)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) findRebindCandidateTx(ctx context.Context, tx *sql.Tx, libraryID string, fingerprint string, seenAt time.Time) (*Record, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, library_id, root_type, root_ref, folder_path, content_fingerprint,
       title, title_jpn, subtitle, description, artists_json, tags_json,
       language, rating, category, source, source_id, source_token, source_url,
       match_kind, confidence, manual_locked, cover_source_url, cover_blob_relpath,
       hint_json, extra_json, last_error, fetched_at, stale_after, last_seen_at, missing_since
FROM manga_metadata
WHERE library_id = ? AND content_fingerprint = ? AND last_seen_at < ?
`, libraryID, fingerprint, formatTime(seenAt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := []Record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) != 1 {
		return nil, nil
	}
	return &matches[0], nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(scanner rowScanner) (Record, error) {
	var rec Record
	var artistsJSON sql.NullString
	var tagsJSON sql.NullString
	var hintJSON sql.NullString
	var extraJSON sql.NullString
	var title sql.NullString
	var titleJPN sql.NullString
	var subtitle sql.NullString
	var description sql.NullString
	var language sql.NullString
	var category sql.NullString
	var source sql.NullString
	var sourceID sql.NullString
	var sourceToken sql.NullString
	var sourceURL sql.NullString
	var matchKind sql.NullString
	var coverSourceURL sql.NullString
	var coverBlobRelpath sql.NullString
	var lastError sql.NullString
	var contentFingerprint sql.NullString
	var folderPath sql.NullString
	var rootRef sql.NullString
	var rootType sql.NullString
	var libraryID sql.NullString
	var fetchedAt sql.NullString
	var staleAfter sql.NullString
	var lastSeenAt sql.NullString
	var missingSince sql.NullString
	var rating sql.NullFloat64
	var confidence sql.NullFloat64
	var manualLocked int

	err := scanner.Scan(
		&rec.ID, &libraryID, &rootType, &rootRef, &folderPath, &contentFingerprint,
		&title, &titleJPN, &subtitle, &description, &artistsJSON, &tagsJSON,
		&language, &rating, &category, &source, &sourceID, &sourceToken, &sourceURL,
		&matchKind, &confidence, &manualLocked, &coverSourceURL, &coverBlobRelpath,
		&hintJSON, &extraJSON, &lastError, &fetchedAt, &staleAfter, &lastSeenAt, &missingSince,
	)
	if err != nil {
		return Record{}, err
	}
	rec.LibraryID = libraryID.String
	rec.RootType = rootType.String
	rec.RootRef = rootRef.String
	rec.FolderPath = folderPath.String
	rec.ContentFingerprint = contentFingerprint.String
	rec.Title = title.String
	rec.TitleJPN = titleJPN.String
	rec.Subtitle = subtitle.String
	rec.Description = description.String
	rec.Language = language.String
	rec.Category = category.String
	rec.Source = source.String
	rec.SourceID = sourceID.String
	rec.SourceToken = sourceToken.String
	rec.SourceURL = sourceURL.String
	rec.MatchKind = matchKind.String
	rec.ManualLocked = manualLocked != 0
	rec.CoverSourceURL = coverSourceURL.String
	rec.CoverBlobRelpath = coverBlobRelpath.String
	rec.ExtraJSON = extraJSON.String
	rec.LastError = lastError.String
	if rating.Valid {
		rec.Rating = rating.Float64
		rec.HasRating = true
	}
	if confidence.Valid {
		rec.Confidence = confidence.Float64
		rec.HasConfidence = true
	}
	if strings.TrimSpace(artistsJSON.String) != "" {
		_ = json.Unmarshal([]byte(artistsJSON.String), &rec.Artists)
	}
	if strings.TrimSpace(tagsJSON.String) != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &rec.Tags)
	}
	if strings.TrimSpace(hintJSON.String) != "" {
		_ = json.Unmarshal([]byte(hintJSON.String), &rec.Hint)
	}
	rec.FetchedAt = parseTimePtr(fetchedAt.String)
	rec.StaleAfter = parseTimePtr(staleAfter.String)
	rec.LastSeenAt = parseTimePtr(lastSeenAt.String)
	rec.MissingSince = parseTimePtr(missingSince.String)
	return rec, nil
}

func emptyStateWhere() string {
	return `COALESCE(title, '') = '' AND COALESCE(title_jpn, '') = '' AND COALESCE(subtitle, '') = '' AND ` +
		`COALESCE(description, '') = '' AND COALESCE(artists_json, '') = '' AND COALESCE(tags_json, '') = '' AND ` +
		`COALESCE(language, '') = '' AND rating IS NULL AND COALESCE(category, '') = '' AND COALESCE(source, '') = '' AND ` +
		`COALESCE(source_id, '') = '' AND COALESCE(source_url, '') = '' AND COALESCE(cover_source_url, '') = '' AND ` +
		`COALESCE(cover_blob_relpath, '') = '' AND COALESCE(extra_json, '') = ''`
}

func encodeJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal metadata json: %w", err)
	}
	if string(raw) == `null` || string(raw) == `[]` || string(raw) == `{}` {
		return "", nil
	}
	return string(raw), nil
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func escapeLikeValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

const stableTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(value time.Time) string {
	return value.UTC().Format(stableTimeLayout)
}

func formatTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(value.UTC())
}

func ptrTime(value time.Time) *time.Time {
	v := value.UTC()
	return &v
}

func parseTimePtr(raw string) *time.Time {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	for _, layout := range []string{stableTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if value, err := time.Parse(layout, raw); err == nil {
			value = value.UTC()
			return &value
		}
	}
	return nil
}
