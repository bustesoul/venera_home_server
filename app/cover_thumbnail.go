package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"
	"time"

	archivepkg "venera_home_server/archive"
	metadatapkg "venera_home_server/metadata"
)

const (
	coverThumbnailMaxLongEdge = 256
	coverThumbnailJPEGQuality = 80
)

func (a *App) LoadOrCreateCoverThumbnail(ctx context.Context, comic *Comic) (*metadatapkg.CoverThumbnail, error) {
	if a == nil || a.metadataStore == nil || comic == nil || len(comic.Chapters) == 0 {
		return nil, nil
	}
	locator := metadatapkg.Locator{
		LibraryID: comic.LibraryID,
		RootType:  comic.RootType,
		RootRef:   comic.RootRef,
	}
	record, err := a.metadataStore.GetByLocator(ctx, locator)
	if err != nil {
		return nil, err
	}
	currentFingerprint := ""
	if record != nil {
		currentFingerprint = strings.TrimSpace(record.ContentFingerprint)
	}
	thumb, err := a.metadataStore.GetCoverThumbnail(ctx, locator)
	if err != nil {
		return nil, err
	}
	if thumbnailMatchesFingerprint(thumb, currentFingerprint) {
		return thumb, nil
	}

	page, err := a.firstCoverPage(ctx, comic)
	if err != nil {
		return nil, err
	}
	sourceBytes, err := a.readPageBytes(ctx, comic, page)
	if err != nil {
		return nil, err
	}
	data, width, height, err := buildCoverThumbnailJPEG(sourceBytes, coverThumbnailMaxLongEdge, coverThumbnailJPEGQuality)
	if err != nil {
		return nil, err
	}
	created := &metadatapkg.CoverThumbnail{
		Locator:            locator,
		ContentFingerprint: currentFingerprint,
		MIMEType:           "image/jpeg",
		Width:              width,
		Height:             height,
		Data:               data,
		UpdatedAt:          time.Now().UTC(),
	}
	_ = a.metadataStore.UpsertCoverThumbnail(ctx, *created)
	return created, nil
}

func thumbnailMatchesFingerprint(thumb *metadatapkg.CoverThumbnail, fingerprint string) bool {
	if thumb == nil || len(thumb.Data) == 0 || strings.TrimSpace(thumb.MIMEType) == "" {
		return false
	}
	return strings.TrimSpace(thumb.ContentFingerprint) == strings.TrimSpace(fingerprint)
}

func (a *App) firstCoverPage(ctx context.Context, comic *Comic) (PageRef, error) {
	if comic == nil || len(comic.Chapters) == 0 {
		return PageRef{}, fmt.Errorf("cover not found")
	}
	pages, err := a.materializeChapterPages(ctx, comic.Chapters[0])
	if err != nil {
		return PageRef{}, err
	}
	if len(pages) == 0 {
		return PageRef{}, fmt.Errorf("cover not found")
	}
	return pages[0], nil
}

func (a *App) readPageBytes(ctx context.Context, comic *Comic, page PageRef) ([]byte, error) {
	if a == nil || comic == nil {
		return nil, fmt.Errorf("comic not found")
	}
	backend := a.backends[comic.LibraryID]
	if backend == nil {
		return nil, fmt.Errorf("backend not found")
	}
	switch page.SourceType {
	case "file":
		rc, _, _, err := backend.OpenStream(ctx, page.SourceRef)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	case "archive":
		archive, err := archivepkg.Open(ctx, backend, page.SourceRef, a.cfg.Server.CacheDir)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		rc, err := archive.Open(ctx, page.EntryName)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	default:
		return nil, fmt.Errorf("unsupported media source type: %s", page.SourceType)
	}
}

func buildCoverThumbnailJPEG(source []byte, maxLongEdge int, quality int) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, 0, 0, err
	}
	resized := resizeImageLongEdgeBilinear(img, maxLongEdge)
	bounds := resized.Bounds()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flattenImageOnWhite(resized), &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), bounds.Dx(), bounds.Dy(), nil
}

func resizeImageLongEdgeBilinear(src image.Image, maxLongEdge int) image.Image {
	bounds := src.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()
	if srcWidth <= 0 || srcHeight <= 0 {
		return src
	}
	longEdge := srcWidth
	if srcHeight > longEdge {
		longEdge = srcHeight
	}
	if longEdge <= maxLongEdge {
		return src
	}
	scale := float64(maxLongEdge) / float64(longEdge)
	dstWidth := int(math.Round(float64(srcWidth) * scale))
	dstHeight := int(math.Round(float64(srcHeight) * scale))
	if dstWidth < 1 {
		dstWidth = 1
	}
	if dstHeight < 1 {
		dstHeight = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	for y := 0; y < dstHeight; y++ {
		sy := (float64(y)+0.5)*float64(srcHeight)/float64(dstHeight) - 0.5
		y0 := int(math.Floor(sy))
		if y0 < 0 {
			y0 = 0
		}
		y1 := y0 + 1
		if y1 >= srcHeight {
			y1 = srcHeight - 1
		}
		fy := sy - float64(y0)
		if fy < 0 {
			fy = 0
		}
		if fy > 1 {
			fy = 1
		}
		for x := 0; x < dstWidth; x++ {
			sx := (float64(x)+0.5)*float64(srcWidth)/float64(dstWidth) - 0.5
			x0 := int(math.Floor(sx))
			if x0 < 0 {
				x0 = 0
			}
			x1 := x0 + 1
			if x1 >= srcWidth {
				x1 = srcWidth - 1
			}
			fx := sx - float64(x0)
			if fx < 0 {
				fx = 0
			}
			if fx > 1 {
				fx = 1
			}

			r00, g00, b00, a00 := src.At(bounds.Min.X+x0, bounds.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := src.At(bounds.Min.X+x1, bounds.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := src.At(bounds.Min.X+x0, bounds.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := src.At(bounds.Min.X+x1, bounds.Min.Y+y1).RGBA()

			interp := func(c00, c10, c01, c11 uint32) uint8 {
				top := (1-fx)*float64(c00) + fx*float64(c10)
				bottom := (1-fx)*float64(c01) + fx*float64(c11)
				v := (1-fy)*top + fy*bottom
				return uint8(math.Round(v / 257.0))
			}

			dst.SetRGBA(x, y, color.RGBA{
				R: interp(r00, r10, r01, r11),
				G: interp(g00, g10, g01, g11),
				B: interp(b00, b10, b01, b11),
				A: interp(a00, a10, a01, a11),
			})
		}
	}
	return dst
}

func flattenImageOnWhite(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}
