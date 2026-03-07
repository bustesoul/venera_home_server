package shared

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

func IsImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func IsArchiveFile(name string) bool {
	return archiveExts[strings.ToLower(filepath.Ext(name))]
}

func CleanRel(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func RelJoin(parts ...string) string {
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = path.Join(out, CleanRel(part))
	}
	return CleanRel(out)
}

func BaseNameTitle(p string) string {
	base := path.Base(strings.ReplaceAll(CleanRel(p), "\\", "/"))
	ext := path.Ext(base)
	if ext != "" && (IsArchiveFile(base) || IsImageFile(base)) {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "." || base == "" || base == "/" {
		return "Library"
	}
	return base
}

func SortedStringsNatural(items []string) []string {
	out := append([]string(nil), items...)
	sort.Slice(out, func(i, j int) bool {
		return NaturalLess(out[i], out[j])
	})
	return out
}

func NaturalLess(a, b string) bool {
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

func ShareAnyFold(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if strings.EqualFold(left, right) {
				return true
			}
		}
	}
	return false
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

func SHAID(parts ...string) string {
	mac := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(mac[:12])
}

type SignedPayload struct {
	Type      string `json:"type"`
	ComicID   string `json:"comic_id,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	PageIndex int    `json:"page_index,omitempty"`
}

func SignPayload(secret string, payload SignedPayload) (string, error) {
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

func ParseSignedPayload(secret, token string) (*SignedPayload, error) {
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
	var payload SignedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func GuessContentType(name string) string {
	c := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if c == "" {
		return "application/octet-stream"
	}
	return c
}

func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func CopyFile(dst string, src io.Reader) error {
	if err := EnsureDir(filepath.Dir(dst)); err != nil {
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

func UniqueStrings(items []string) []string {
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
