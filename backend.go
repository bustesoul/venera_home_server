package main

import (
    "context"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "time"
)

type Entry struct {
    Name    string
    RelPath string
    IsDir   bool
    Size    int64
    ModTime time.Time
}

type Backend interface {
    Kind() string
    Connect(context.Context) error
    ListDir(context.Context, string) ([]Entry, error)
    ReadSmallFile(context.Context, string, int64) ([]byte, error)
    ReaderAt(context.Context, string) (io.ReaderAt, io.Closer, int64, error)
    OpenStream(context.Context, string) (io.ReadCloser, int64, time.Time, error)
}

type localBackend struct {
    kind string
    root string
}

func newLocalBackend(root string) Backend {
    return &localBackend{kind: "local", root: filepath.Clean(root)}
}

func (b *localBackend) Kind() string { return b.kind }
func (b *localBackend) Connect(context.Context) error { return nil }

func (b *localBackend) abs(rel string) string {
    rel = strings.ReplaceAll(cleanRel(rel), "/", string(filepath.Separator))
    if rel == "" {
        return b.root
    }
    return filepath.Join(b.root, rel)
}

func (b *localBackend) ListDir(_ context.Context, rel string) ([]Entry, error) {
    items, err := os.ReadDir(b.abs(rel))
    if err != nil {
        return nil, err
    }
    out := make([]Entry, 0, len(items))
    for _, item := range items {
        info, err := item.Info()
        if err != nil {
            return nil, err
        }
        out = append(out, Entry{
            Name: item.Name(),
            RelPath: relJoin(rel, item.Name()),
            IsDir: item.IsDir(),
            Size: info.Size(),
            ModTime: info.ModTime(),
        })
    }
    sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
    return out, nil
}

func (b *localBackend) ReadSmallFile(_ context.Context, rel string, max int64) ([]byte, error) {
    path := b.abs(rel)
    info, err := os.Stat(path)
    if err != nil {
        return nil, err
    }
    if max > 0 && info.Size() > max {
        return nil, fmt.Errorf("file too large")
    }
    return os.ReadFile(path)
}

func (b *localBackend) ReaderAt(_ context.Context, rel string) (io.ReaderAt, io.Closer, int64, error) {
    f, err := os.Open(b.abs(rel))
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

func (b *localBackend) OpenStream(_ context.Context, rel string) (io.ReadCloser, int64, time.Time, error) {
    f, err := os.Open(b.abs(rel))
    if err != nil {
        return nil, 0, time.Time{}, err
    }
    info, err := f.Stat()
    if err != nil {
        _ = f.Close()
        return nil, 0, time.Time{}, err
    }
    return f, info.Size(), info.ModTime(), nil
}
