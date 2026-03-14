package metadata

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"time"
)

type CoverThumbnail struct {
	Locator            Locator   `json:"locator"`
	ContentFingerprint string    `json:"content_fingerprint,omitempty"`
	MIMEType           string    `json:"mime_type,omitempty"`
	Width              int       `json:"width,omitempty"`
	Height             int       `json:"height,omitempty"`
	Data               []byte    `json:"-"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func coverThumbnailDBPath(mainPath string) string {
	ext := filepath.Ext(mainPath)
	base := strings.TrimSuffix(mainPath, ext)
	if base == "" {
		return mainPath + ".thumbs.db"
	}
	return base + ".thumbs.db"
}

func initCoverThumbnailSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS cover_thumbnails (
    library_id TEXT NOT NULL,
    root_type TEXT NOT NULL,
    root_ref TEXT NOT NULL,
    content_fingerprint TEXT,
    mime_type TEXT NOT NULL,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    image_blob BLOB NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(library_id, root_type, root_ref)
);
CREATE INDEX IF NOT EXISTS idx_cover_thumbnails_updated_at ON cover_thumbnails(updated_at DESC);
`)
	return err
}

func (s *Store) GetCoverThumbnail(ctx context.Context, locator Locator) (*CoverThumbnail, error) {
	if s == nil || s.thumbDB == nil || !locator.Valid() {
		return nil, nil
	}
	row := s.thumbDB.QueryRowContext(ctx, `
SELECT library_id, root_type, root_ref, content_fingerprint, mime_type, width, height, image_blob, updated_at
FROM cover_thumbnails
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, locator.LibraryID, locator.RootType, locator.RootRef)
	var item CoverThumbnail
	var fingerprint sql.NullString
	var updatedAt sql.NullString
	err := row.Scan(
		&item.Locator.LibraryID,
		&item.Locator.RootType,
		&item.Locator.RootRef,
		&fingerprint,
		&item.MIMEType,
		&item.Width,
		&item.Height,
		&item.Data,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	item.ContentFingerprint = fingerprint.String
	if parsed := parseTimePtr(updatedAt.String); parsed != nil {
		item.UpdatedAt = *parsed
	}
	return &item, nil
}

func (s *Store) UpsertCoverThumbnail(ctx context.Context, item CoverThumbnail) error {
	if s == nil || s.thumbDB == nil || !item.Locator.Valid() {
		return nil
	}
	item.MIMEType = strings.TrimSpace(item.MIMEType)
	if len(item.Data) == 0 || item.MIMEType == "" {
		return nil
	}
	if item.Width <= 0 || item.Height <= 0 {
		return nil
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	} else {
		item.UpdatedAt = item.UpdatedAt.UTC()
	}
	_, err := s.thumbDB.ExecContext(ctx, `
INSERT INTO cover_thumbnails(
	library_id, root_type, root_ref, content_fingerprint, mime_type, width, height, image_blob, updated_at
) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, root_type, root_ref) DO UPDATE SET
	content_fingerprint = excluded.content_fingerprint,
	mime_type = excluded.mime_type,
	width = excluded.width,
	height = excluded.height,
	image_blob = excluded.image_blob,
	updated_at = excluded.updated_at
`,
		item.Locator.LibraryID,
		item.Locator.RootType,
		item.Locator.RootRef,
		nullIfEmpty(item.ContentFingerprint),
		item.MIMEType,
		item.Width,
		item.Height,
		item.Data,
		formatTime(item.UpdatedAt),
	)
	return err
}

func (s *Store) DeleteCoverThumbnail(ctx context.Context, locator Locator) error {
	if s == nil || s.thumbDB == nil || !locator.Valid() {
		return nil
	}
	_, err := s.thumbDB.ExecContext(ctx, `
DELETE FROM cover_thumbnails
WHERE library_id = ? AND root_type = ? AND root_ref = ?
`, locator.LibraryID, locator.RootType, locator.RootRef)
	return err
}
