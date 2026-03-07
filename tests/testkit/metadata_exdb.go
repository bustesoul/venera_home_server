package testkit

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

type ExDBGalleryRow struct {
	GID      string
	Token    string
	Title    string
	TitleJPN string
	Artist   string
	Category string
	Rating   float64
	Thumb    string
}

func MustSeedExDBGallery(t *testing.T, path string, rows []ExDBGalleryRow) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS gallery (
    gid TEXT,
    token TEXT,
    title TEXT,
    title_jpn TEXT,
    artist TEXT,
    category TEXT,
    rating REAL,
    thumb TEXT
);
DELETE FROM gallery;
`)
	if err != nil {
		t.Fatalf("create gallery table: %v", err)
	}
	stmt, err := db.Prepare(`INSERT INTO gallery(gid, token, title, title_jpn, artist, category, rating, thumb) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare insert: %v", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.Exec(row.GID, row.Token, row.Title, row.TitleJPN, row.Artist, row.Category, row.Rating, row.Thumb); err != nil {
			t.Fatalf("insert gallery row: %v", err)
		}
	}
}
