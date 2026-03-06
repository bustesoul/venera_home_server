package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveReadersOpenRarAnd7z(t *testing.T) {
	root := t.TempDir()
	mustCopyFixture(t, "test.rar", filepath.Join(root, "Sample.cbr"))
	mustCopyFixture(t, "copy.7z", filepath.Join(root, "Sample.cb7"))

	backend := newLocalBackend(root)
	cacheDir := filepath.Join(root, "cache")
	if err := ensureDir(cacheDir); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		rel    string
		format string
	}{
		{rel: "Sample.cbr", format: "rar"},
		{rel: "Sample.cb7", format: "7z"},
	}
	for _, tc := range tests {
		archive, err := openArchive(context.Background(), backend, tc.rel, cacheDir)
		if err != nil {
			t.Fatalf("openArchive(%s): %v", tc.rel, err)
		}
		if archive.Format() != tc.format {
			t.Fatalf("archive %s format = %s, want %s", tc.rel, archive.Format(), tc.format)
		}
		entries := archive.Entries()
		if len(entries) == 0 {
			t.Fatalf("archive %s returned no entries", tc.rel)
		}
		var target *ArchiveEntry
		for i := range entries {
			if !entries[i].IsDir {
				target = &entries[i]
				break
			}
		}
		if target == nil {
			t.Fatalf("archive %s returned no file entries", tc.rel)
		}
		rc, err := archive.Open(context.Background(), target.Name)
		if err != nil {
			t.Fatalf("archive.Open(%s, %s): %v", tc.rel, target.Name, err)
		}
		buf := make([]byte, 32)
		n, err := rc.Read(buf)
		_ = rc.Close()
		_ = archive.Close()
		if err != nil && err != io.EOF {
			t.Fatalf("archive.Read(%s): %v", tc.rel, err)
		}
		if n == 0 {
			t.Fatalf("archive %s entry %s returned no data", tc.rel, target.Name)
		}
	}
}

func TestPDFComicFlow(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("pdf rendering currently requires Windows runtime")
	}

	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	mustWriteMinimalPDF(t, filepath.Join(libraryRoot, "PDF Book.pdf"), "Hello PDF")

	cfg := &Config{
		Server:    ServerConfig{Listen: "127.0.0.1:0", Token: "test-token", DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache")},
		Scan:      ScanConfig{Concurrency: 1, ExtractArchives: true},
		Metadata:  MetadataConfig{ReadComicInfo: true, ReadSidecar: true},
		Libraries: []LibraryConfig{{ID: "local-main", Name: "Local", Kind: "local", Root: libraryRoot, ScanMode: "auto"}},
	}
	app, err := NewApp(cfg)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	ids := app.libraries["local-main"]
	if len(ids) != 1 {
		t.Fatalf("expected 1 pdf comic, got %d", len(ids))
	}
	comic := app.comics[ids[0]]
	if comic.Title != "PDF Book" {
		t.Fatalf("unexpected comic title: %s", comic.Title)
	}
	chapter := comic.Chapters[0]
	pages, err := app.materializeChapterPages(context.Background(), chapter)
	if err != nil {
		t.Fatalf("materializeChapterPages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 pdf page, got %d", len(pages))
	}

	srv := httptest.NewServer(newHTTPServer(app, log.New(io.Discard, "", 0)))
	defer srv.Close()

	pagesResp := getJSON(t, fmt.Sprintf("%s/api/v1/comics/%s/chapters/%s/pages", srv.URL, comic.ID, chapter.ID), cfg.Server.Token)
	images := pagesResp["data"].(map[string]any)["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("expected 1 page image, got %d", len(images))
	}

	req, _ := http.NewRequest(http.MethodGet, images[0].(string), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("media request: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8))
	if !bytes.Equal(raw, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) {
		t.Fatalf("expected PNG header, got %v", raw)
	}
}

func mustCopyFixture(t *testing.T, name, target string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, target, raw)
}

func mustWriteMinimalPDF(t *testing.T, target, text string) {
	t.Helper()
	text = strings.ReplaceAll(text, "\\", "\\\\")
	text = strings.ReplaceAll(text, "(", "\\(")
	text = strings.ReplaceAll(text, ")", "\\)")

	header := []byte("%PDF-1.4\n")
	stream := []byte(fmt.Sprintf("BT /F1 24 Tf 72 100 Td (%s) Tj ET", text))
	objects := [][]byte{
		[]byte("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"),
		[]byte("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n"),
		[]byte("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n"),
		[]byte(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream)),
		[]byte("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n"),
	}

	output := make([]byte, 0, 1024)
	output = append(output, header...)
	offsets := make([]int, 0, len(objects))
	for _, object := range objects {
		offsets = append(offsets, len(output))
		output = append(output, object...)
	}
	xrefStart := len(output)
	output = append(output, []byte("xref\n0 6\n")...)
	output = append(output, []byte("0000000000 65535 f \n")...)
	for _, offset := range offsets {
		output = append(output, []byte(fmt.Sprintf("%010d 00000 n \n", offset))...)
	}
	output = append(output, []byte(fmt.Sprintf("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefStart))...)
	mustWriteFile(t, target, output)
}
