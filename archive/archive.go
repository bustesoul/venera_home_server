package archive

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	backendpkg "venera_home_server/backend"
	"venera_home_server/shared"

	sevenzip "github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

type ArchiveEntry struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

type Archive interface {
	Format() string
	Entries() []ArchiveEntry
	Open(context.Context, string) (io.ReadCloser, error)
	Close() error
}

func Open(ctx context.Context, backend backendpkg.Backend, rel, cacheDir string) (Archive, error) {
	switch strings.ToLower(path.Ext(rel)) {
	case ".cbz", ".zip":
		return openZIPArchive(ctx, backend, rel)
	case ".cbr", ".rar":
		return openRARArchive(ctx, backend, rel, cacheDir)
	case ".cb7", ".7z":
		return openSevenZipArchive(ctx, backend, rel)
	case ".pdf":
		return openPDFArchive(ctx, backend, rel, cacheDir)
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", rel)
	}
}

func materializeArchiveSource(ctx context.Context, backend backendpkg.Backend, rel, cacheDir string) (string, string, int64, error) {
	readerAt, closer, size, err := backend.ReaderAt(ctx, rel)
	if err != nil {
		return "", "", 0, err
	}
	defer closer.Close()

	key := shared.SHAID(shared.CleanRel(rel), strconv.FormatInt(size, 10))
	target := filepath.Join(cacheDir, "archive-source", key+strings.ToLower(path.Ext(rel)))
	if _, err := os.Stat(target); err == nil {
		return target, key, size, nil
	}
	if err := shared.CopyFile(target, io.NewSectionReader(readerAt, 0, size)); err != nil {
		return "", "", 0, err
	}
	return target, key, size, nil
}

type zipArchive struct {
	closer  io.Closer
	files   []*zip.File
	entries []ArchiveEntry
}

func openZIPArchive(ctx context.Context, backend backendpkg.Backend, rel string) (Archive, error) {
	readerAt, closer, size, err := backend.ReaderAt(ctx, rel)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(readerAt, size)
	if err != nil {
		_ = closer.Close()
		return nil, err
	}
	entries := make([]ArchiveEntry, 0, len(zr.File))
	for _, file := range zr.File {
		info := file.FileInfo()
		entries = append(entries, ArchiveEntry{
			Name:    file.Name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
	}
	return &zipArchive{closer: closer, files: zr.File, entries: entries}, nil
}

func (a *zipArchive) Format() string { return "zip" }
func (a *zipArchive) Entries() []ArchiveEntry {
	return append([]ArchiveEntry(nil), a.entries...)
}
func (a *zipArchive) Open(_ context.Context, name string) (io.ReadCloser, error) {
	for _, file := range a.files {
		if file.Name == name {
			return file.Open()
		}
	}
	return nil, os.ErrNotExist
}
func (a *zipArchive) Close() error { return a.closer.Close() }

type sevenZipArchive struct {
	closer  io.Closer
	files   []*sevenzip.File
	entries []ArchiveEntry
}

func openSevenZipArchive(ctx context.Context, backend backendpkg.Backend, rel string) (Archive, error) {
	readerAt, closer, size, err := backend.ReaderAt(ctx, rel)
	if err != nil {
		return nil, err
	}
	zr, err := sevenzip.NewReader(readerAt, size)
	if err != nil {
		_ = closer.Close()
		return nil, err
	}
	entries := make([]ArchiveEntry, 0, len(zr.File))
	for _, file := range zr.File {
		info := file.FileInfo()
		entries = append(entries, ArchiveEntry{
			Name:    file.Name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
	}
	return &sevenZipArchive{closer: closer, files: zr.File, entries: entries}, nil
}

func (a *sevenZipArchive) Format() string { return "7z" }
func (a *sevenZipArchive) Entries() []ArchiveEntry {
	return append([]ArchiveEntry(nil), a.entries...)
}
func (a *sevenZipArchive) Open(_ context.Context, name string) (io.ReadCloser, error) {
	for _, file := range a.files {
		if file.Name == name {
			return file.Open()
		}
	}
	return nil, os.ErrNotExist
}
func (a *sevenZipArchive) Close() error { return a.closer.Close() }

type rarArchive struct {
	sourcePath string
	entries    []ArchiveEntry
}

func openRARArchive(ctx context.Context, backend backendpkg.Backend, rel, cacheDir string) (Archive, error) {
	sourcePath, _, _, err := materializeArchiveSource(ctx, backend, rel, cacheDir)
	if err != nil {
		return nil, err
	}
	rc, err := rardecode.OpenReader(sourcePath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	entries := []ArchiveEntry{}
	for {
		header, err := rc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		size := header.UnPackedSize
		if header.UnKnownSize {
			size = 0
		}
		entries = append(entries, ArchiveEntry{
			Name:    header.Name,
			Size:    size,
			ModTime: header.ModificationTime,
			IsDir:   header.IsDir,
		})
	}
	return &rarArchive{sourcePath: sourcePath, entries: entries}, nil
}

func (a *rarArchive) Format() string { return "rar" }
func (a *rarArchive) Entries() []ArchiveEntry {
	return append([]ArchiveEntry(nil), a.entries...)
}
func (a *rarArchive) Open(_ context.Context, name string) (io.ReadCloser, error) {
	rc, err := rardecode.OpenReader(a.sourcePath)
	if err != nil {
		return nil, err
	}
	for {
		header, err := rc.Next()
		if err == io.EOF {
			_ = rc.Close()
			return nil, os.ErrNotExist
		}
		if err != nil {
			_ = rc.Close()
			return nil, err
		}
		if !header.IsDir && header.Name == name {
			return rc, nil
		}
	}
}
func (a *rarArchive) Close() error { return nil }
