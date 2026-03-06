package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
	".gif":  true,
	".bmp":  true,
	".avif": true,
}

var archiveExts = map[string]bool{
	".cbz": true,
	".zip": true,
	".cbr": true,
	".rar": true,
	".cb7": true,
	".7z":  true,
	".pdf": true,
}

func isImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func isArchiveFile(name string) bool {
	return archiveExts[strings.ToLower(filepath.Ext(name))]
}

func cleanRel(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func relJoin(parts ...string) string {
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = path.Join(out, cleanRel(part))
	}
	return cleanRel(out)
}

func baseNameTitle(p string) string {
	base := path.Base(strings.ReplaceAll(cleanRel(p), "\\", "/"))
	ext := path.Ext(base)
	if ext != "" && (isArchiveFile(base) || isImageFile(base)) {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "." || base == "" || base == "/" {
		return "Library"
	}
	return base
}

func sortedStringsNatural(items []string) []string {
	out := append([]string(nil), items...)
	sort.Slice(out, func(i, j int) bool {
		return naturalLess(out[i], out[j])
	})
	return out
}

func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	ar, br := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	for ai < len(ar) && bi < len(br) {
		if isDigit(ar[ai]) && isDigit(br[bi]) {
			an, ni := readNumber(ar, ai)
			bn, nj := readNumber(br, bi)
			if an != bn {
				return an < bn
			}
			ai, bi = ni, nj
			continue
		}
		if ar[ai] != br[bi] {
			return ar[ai] < br[bi]
		}
		ai++
		bi++
	}
	return len(ar) < len(br)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func readNumber(items []rune, start int) (int, int) {
	end := start
	for end < len(items) && isDigit(items[end]) {
		end++
	}
	n, _ := strconv.Atoi(string(items[start:end]))
	return n, end
}

func shaID(parts ...string) string {
	mac := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(mac[:12])
}

type signedPayload struct {
	Type      string `json:"type"`
	ComicID   string `json:"comic_id,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	PageIndex int    `json:"page_index,omitempty"`
}

func signPayload(secret string, payload signedPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	data := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(data))
	sig := hex.EncodeToString(mac.Sum(nil))
	return data + "." + sig, nil
}

func parseSignedPayload(secret, token string) (*signedPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[1])) {
		return nil, fmt.Errorf("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var payload signedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func guessContentType(name string) string {
	c := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if c == "" {
		return "application/octet-stream"
	}
	return c
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func copyFile(dst string, src io.Reader) error {
	if err := ensureDir(filepath.Dir(dst)); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, src)
	return err
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
