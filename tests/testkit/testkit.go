package testkit

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func MustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func MustWriteZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func GetJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("unexpected status %d: %s", res.StatusCode, string(raw))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func PostJSON(t *testing.T, url, token string, payload map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("unexpected status %d: %s", res.StatusCode, string(body))
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func WaitForRenderedCacheCount(root string, wantAtLeast int, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		cachedPages, err := filepath.Glob(filepath.Join(root, "cache", "rendered-pages", "*"))
		if err != nil {
			return nil, fmt.Errorf("glob cache files: %w", err)
		}
		if len(cachedPages) >= wantAtLeast {
			return cachedPages, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("expected at least %d rendered cache files, got %d", wantAtLeast, len(cachedPages))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func MustCopyFixture(t *testing.T, name, target string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve fixture path failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "archive_testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	MustWriteFile(t, target, raw)
}

func MustWriteMinimalPDF(t *testing.T, target, text string) {
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
	MustWriteFile(t, target, output)
}

func MustWriteSolidJPEG(t *testing.T, target string, width int, height int, fill color.Color) {
	t.Helper()
	if width <= 0 || height <= 0 {
		t.Fatal("invalid jpeg size")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	MustWriteFile(t, target, buf.Bytes())
}

type DAVTestItem struct {
	Href  string
	IsDir bool
	Size  int64
}

func WriteDAVResponse(w http.ResponseWriter, base string, items []DAVTestItem) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><multistatus xmlns="DAV:">`)
	for _, item := range items {
		resource := "<resourcetype/>"
		if item.IsDir {
			resource = `<resourcetype><collection/></resourcetype>`
		}
		_, _ = io.WriteString(w, fmt.Sprintf(`<response><href>%s%s</href><propstat><prop>%s<getcontentlength>%d</getcontentlength><getlastmodified>Mon, 06 Mar 2026 12:00:00 GMT</getlastmodified></prop></propstat></response>`, base, item.Href, resource, item.Size))
	}
	_, _ = io.WriteString(w, `</multistatus>`)
}
