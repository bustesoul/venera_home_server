package exdbdryrun_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"venera_home_server/exdbdryrun"
	metadatapkg "venera_home_server/metadata"
)

func TestRunMatchesByGIDToken(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := metadatapkg.OpenStore(filepath.Dir(metadataPath), metadataPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.Close()
	insertLocalRecord(t, metadataPath, map[string]any{
		"library_id":   "lib-a",
		"root_type":    "folder",
		"root_ref":     "Shelf/Book A",
		"folder_path":  "Shelf/Book A",
		"source_id":    "12345",
		"source_token": "tok-abc",
	})

	exdbPath := filepath.Join(t.TempDir(), "exdb.sqlite")
	db := openSQLite(t, exdbPath)
	mustExec(t, db, `CREATE TABLE galleries (id INTEGER PRIMARY KEY, gid TEXT, token TEXT, title TEXT, title_jpn TEXT, artists TEXT, tags TEXT)`)
	mustExec(t, db, `INSERT INTO galleries (gid, token, title, title_jpn, artists, tags) VALUES ('12345', 'tok-abc', 'English Title', 'Japanese Title', 'artist-a', 'tag-a tag-b')`)
	db.Close()

	report, err := exdbdryrun.Run(context.Background(), exdbdryrun.Config{
		MetadataDBPath: metadataPath,
		ExDBPath:       exdbPath,
		LibraryID:      "lib-a",
		State:          "all",
		Limit:          10,
		MinScore:       0.7,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Schema.ChosenTable != "galleries" {
		t.Fatalf("expected chosen table galleries, got %q", report.Schema.ChosenTable)
	}
	if report.Summary.Matched != 1 {
		t.Fatalf("expected 1 matched record, got %+v", report.Summary)
	}
	if report.Matches[0].Match == nil {
		t.Fatal("expected a match")
	}
	if report.Matches[0].Match.Method != "gid_token" {
		t.Fatalf("expected gid_token match, got %#v", report.Matches[0].Match)
	}
}

func TestRunFallsBackToFolderTitle(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := metadatapkg.OpenStore(filepath.Dir(metadataPath), metadataPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.Close()
	insertLocalRecord(t, metadataPath, map[string]any{
		"library_id":  "lib-b",
		"root_type":   "folder",
		"root_ref":    "Shelf/Blue Archive",
		"folder_path": "Shelf/Blue Archive",
	})

	exdbPath := filepath.Join(t.TempDir(), "exdb.sqlite")
	db := openSQLite(t, exdbPath)
	mustExec(t, db, `CREATE TABLE galleries (gid TEXT, token TEXT, title TEXT, artists TEXT, tags TEXT)`)
	mustExec(t, db, `INSERT INTO galleries (gid, token, title, artists, tags) VALUES ('9', 't', 'Blue Archive', 'artist-b', 'school action')`)
	db.Close()

	report, err := exdbdryrun.Run(context.Background(), exdbdryrun.Config{
		MetadataDBPath: metadataPath,
		ExDBPath:       exdbPath,
		LibraryID:      "lib-b",
		State:          "all",
		Limit:          10,
		MinScore:       0.7,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Summary.Matched != 1 {
		t.Fatalf("expected 1 matched record, got %+v", report.Summary)
	}
	if report.Matches[0].Match == nil {
		t.Fatal("expected a match")
	}
	if got := report.Matches[0].Match.Method; got != "title_exact" && got != "folder_exact" {
		t.Fatalf("expected title_exact or folder_exact, got %#v", report.Matches[0].Match)
	}
}

func TestRunKeepsUsefulFuzzyMatches(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := metadatapkg.OpenStore(filepath.Dir(metadataPath), metadataPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.Close()
	insertLocalRecord(t, metadataPath, map[string]any{
		"library_id":  "lib-fuzzy",
		"root_type":   "archive",
		"root_ref":    "Shelf/[Ashiomi Masato] Kioku Ryoujoku [Chinese] [DL].zip",
		"folder_path": "Shelf/[Ashiomi Masato] Kioku Ryoujoku [Chinese] [DL].zip",
	})

	exdbPath := filepath.Join(t.TempDir(), "exdb.sqlite")
	db := openSQLite(t, exdbPath)
	mustExec(t, db, `CREATE TABLE galleries (gid TEXT, token TEXT, title TEXT, title_jpn TEXT, artists TEXT, tags TEXT)`)
	mustExec(t, db, `INSERT INTO galleries (gid, token, title, title_jpn, artists, tags) VALUES ('77', 'tok', '[Ashiomi Masato] Kioku Ryoujoku', 'Kioku Ryoujoku', 'ashiomi masato', 'manga')`)
	db.Close()

	report, err := exdbdryrun.Run(context.Background(), exdbdryrun.Config{
		MetadataDBPath: metadataPath,
		ExDBPath:       exdbPath,
		LibraryID:      "lib-fuzzy",
		State:          "all",
		Limit:          10,
		MinScore:       0.7,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Summary.Matched != 1 {
		t.Fatalf("expected 1 matched record, got %+v", report.Summary)
	}
	if report.Matches[0].Match == nil || report.Matches[0].Match.Method != "title_fuzzy" {
		t.Fatalf("expected useful fuzzy match, got %#v", report.Matches[0].Match)
	}
}

func TestRunRejectsGenericNoTextOnlyMatch(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := metadatapkg.OpenStore(filepath.Dir(metadataPath), metadataPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	store.Close()
	insertLocalRecord(t, metadataPath, map[string]any{
		"library_id":  "lib-c",
		"root_type":   "dir",
		"root_ref":    "Shelf/RJ01375986/No Text",
		"folder_path": "Shelf/RJ01375986/No Text",
	})

	exdbPath := filepath.Join(t.TempDir(), "exdb.sqlite")
	db := openSQLite(t, exdbPath)
	mustExec(t, db, `CREATE TABLE gallery (gid TEXT, token TEXT, title TEXT, title_jpn TEXT, artist TEXT, category TEXT, rating TEXT, thumb TEXT)`)
	mustExec(t, db, `INSERT INTO gallery (gid, token, title, title_jpn, artist, category, rating, thumb) VALUES ('471167', '3d05311b44', '[Gin] Random Work (No Text)', '', 'gin', 'Artist CG', '2.41', 'https://example.com/thumb.jpg')`)
	db.Close()

	report, err := exdbdryrun.Run(context.Background(), exdbdryrun.Config{
		MetadataDBPath: metadataPath,
		ExDBPath:       exdbPath,
		LibraryID:      "lib-c",
		State:          "all",
		Limit:          10,
		MinScore:       0.7,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Summary.Matched != 0 || report.Summary.Unmatched != 1 {
		t.Fatalf("expected obvious no-text false positive to be rejected, got %+v", report.Summary)
	}
	if report.Matches[0].Match != nil {
		t.Fatalf("expected no match, got %#v", report.Matches[0].Match)
	}
}

func TestInspectChoosesMostLikelyTable(t *testing.T) {
	exdbPath := filepath.Join(t.TempDir(), "exdb.sqlite")
	db := openSQLite(t, exdbPath)
	mustExec(t, db, `CREATE TABLE logs (id INTEGER PRIMARY KEY, message TEXT, created_at TEXT)`)
	mustExec(t, db, `CREATE TABLE galleries (id INTEGER PRIMARY KEY, gid TEXT, token TEXT, title TEXT, title_jpn TEXT, artists TEXT, tags TEXT, cover_url TEXT)`)
	mustExec(t, db, `INSERT INTO galleries (gid, token, title) VALUES ('1', 'a', 'Test Title')`)
	db.Close()

	report, err := exdbdryrun.Run(context.Background(), exdbdryrun.Config{
		ExDBPath:    exdbPath,
		InspectOnly: true,
	})
	if err != nil {
		t.Fatalf("Run inspect: %v", err)
	}
	if report.Schema.ChosenTable != "galleries" {
		t.Fatalf("expected galleries as chosen table, got %q", report.Schema.ChosenTable)
	}
	if len(report.Schema.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(report.Schema.Tables))
	}
}

func insertLocalRecord(t *testing.T, metadataPath string, values map[string]any) {
	t.Helper()
	db := openSQLite(t, metadataPath)
	defer db.Close()
	columns := []string{"library_id", "root_type", "root_ref", "folder_path", "last_seen_at"}
	args := []any{
		values["library_id"],
		values["root_type"],
		values["root_ref"],
		values["folder_path"],
		time.Now().UTC().Format(time.RFC3339Nano),
	}
	for _, optional := range []string{"title", "title_jpn", "source_id", "source_token", "hint_json"} {
		if value, ok := values[optional]; ok {
			columns = append(columns, optional)
			args = append(args, value)
		}
	}
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	stmt := `INSERT INTO manga_metadata (` + strings.Join(columns, ", ") + `) VALUES (` + strings.Join(placeholders, ", ") + `)`
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("insert local record: %v", err)
	}
}

func openSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}
