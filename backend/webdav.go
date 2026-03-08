package backend

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"venera_home_server/config"
	"venera_home_server/shared"
)

type webdavBackend struct {
	baseURL  *url.URL
	basePath string
	username string
	password string
	cacheDir string
	client   *http.Client
}

func NewWebDAVBackend(lib config.LibraryConfig, cacheDir string) (Backend, error) {
	u, err := url.Parse(lib.URL)
	if err != nil {
		return nil, err
	}
	return &webdavBackend{
		baseURL:  u,
		basePath: shared.CleanRel(lib.Path),
		username: lib.Username,
		password: os.Getenv(lib.PasswordEnv),
		cacheDir: filepath.Join(cacheDir, "webdav", lib.ID),
		client:   &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func (b *webdavBackend) Kind() string                  { return "webdav" }
func (b *webdavBackend) Connect(context.Context) error { return shared.EnsureDir(b.cacheDir) }

func (b *webdavBackend) buildURL(rel string) string {
	cp := shared.RelJoin(b.basePath, rel)
	clone := *b.baseURL
	clone.Path = path.Join(b.baseURL.Path, cp)
	return clone.String()
}

func (b *webdavBackend) request(ctx context.Context, method, rel string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.buildURL(rel), body)
	if err != nil {
		return nil, err
	}
	if b.username != "" {
		req.SetBasicAuth(b.username, b.password)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return b.client.Do(req)
}

type davMultiStatus struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string        `xml:"href"`
	PropStat []davPropStat `xml:"propstat"`
}

type davPropStat struct {
	Prop davProp `xml:"prop"`
}

type davProp struct {
	ResourceType  davResourceType `xml:"resourcetype"`
	ContentLength string          `xml:"getcontentlength"`
	LastModified  string          `xml:"getlastmodified"`
}

type davResourceType struct {
	Collection *struct{} `xml:"collection"`
}

func (b *webdavBackend) ListDir(ctx context.Context, rel string) ([]Entry, error) {
	body := bytes.NewBufferString(`<?xml version="1.0" encoding="utf-8" ?><propfind xmlns="DAV:"><prop><resourcetype/><getcontentlength/><getlastmodified/></prop></propfind>`)
	res, err := b.request(ctx, "PROPFIND", rel, body, map[string]string{
		"Depth":        "1",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("webdav list dir: %s", res.Status)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var ms davMultiStatus
	if err := xml.Unmarshal(raw, &ms); err != nil {
		return nil, err
	}
	out := []Entry{}
	current := strings.TrimSuffix(path.Base(strings.TrimSuffix(shared.CleanRel(rel), "/")), "/")
	for _, item := range ms.Responses {
		parsed, err := url.Parse(item.Href)
		if err != nil {
			continue
		}
		name := path.Base(strings.TrimSuffix(parsed.Path, "/"))
		if name == "" || name == current {
			continue
		}
		entry := Entry{Name: name, RelPath: shared.RelJoin(rel, name)}
		for _, ps := range item.PropStat {
			entry.IsDir = entry.IsDir || ps.Prop.ResourceType.Collection != nil
			if ps.Prop.ContentLength != "" {
				fmt.Sscan(ps.Prop.ContentLength, &entry.Size)
			}
			if ps.Prop.LastModified != "" {
				if tm, err := http.ParseTime(ps.Prop.LastModified); err == nil {
					entry.ModTime = tm
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (b *webdavBackend) ReadSmallFile(ctx context.Context, rel string, max int64) ([]byte, error) {
	res, err := b.request(ctx, http.MethodGet, rel, nil, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("webdav read file: %s", res.Status)
	}
	if max > 0 {
		return io.ReadAll(io.LimitReader(res.Body, max+1))
	}
	return io.ReadAll(res.Body)
}

func (b *webdavBackend) ReaderAt(ctx context.Context, rel string) (io.ReaderAt, io.Closer, int64, error) {
	cacheName := filepath.Join(b.cacheDir, shared.SHAID(rel)+filepath.Ext(rel))
	if _, err := os.Stat(cacheName); err != nil {
		res, err := b.request(ctx, http.MethodGet, rel, nil, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, nil, 0, fmt.Errorf("webdav download archive: %s", res.Status)
		}
		if err := shared.CopyFile(cacheName, res.Body); err != nil {
			return nil, nil, 0, err
		}
	}
	f, err := os.Open(cacheName)
	if err != nil {
		return nil, nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, 0, err
	}
	return f, f, info.Size(), nil
}

func (b *webdavBackend) OpenStream(ctx context.Context, rel string) (io.ReadCloser, int64, time.Time, error) {
	res, err := b.request(ctx, http.MethodGet, rel, nil, nil)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, 0, time.Time{}, fmt.Errorf("webdav open stream: %s", res.Status)
	}
	var modTime time.Time
	if header := res.Header.Get("Last-Modified"); header != "" {
		modTime, _ = http.ParseTime(header)
	}
	return res.Body, res.ContentLength, modTime, nil
}

func (b *webdavBackend) WriteSmallFile(ctx context.Context, rel string, data []byte) error {
	res, err := b.request(ctx, http.MethodPut, rel, bytes.NewReader(data), map[string]string{
		"Content-Type": "application/json; charset=utf-8",
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("webdav write file: %s", res.Status)
	}
	return nil
}

func (b *webdavBackend) DeleteFile(ctx context.Context, rel string) error {
	res, err := b.request(ctx, http.MethodDelete, rel, nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("webdav delete file: %s", res.Status)
	}
	return nil
}
