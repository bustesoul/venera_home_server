package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	apppkg "venera_home_server/app"
	archivepkg "venera_home_server/archive"
	backendpkg "venera_home_server/backend"
)

const enableServerSideChapterPrefetch = false
const renderedPageCacheVariant = "mode-hqjpeg-long4096-q90-v5"
const pagePrefetchThrottleStep = 4
const largePageVisualCompressThreshold = 12 * 1024 * 1024
const largePageVisualCompressQuality = 90
const largePageVisualCompressMaxLongEdge = 4096

func prefetchThrottleWindowStart(windowStart int) int {
	if windowStart < 0 {
		return 0
	}
	if pagePrefetchThrottleStep <= 1 {
		return windowStart
	}
	return (windowStart / pagePrefetchThrottleStep) * pagePrefetchThrottleStep
}

func normalizeContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func pageSourceExt(page apppkg.PageRef) string {
	ext := strings.ToLower(filepath.Ext(page.EntryName))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(page.Name))
	}
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(page.SourceRef))
	}
	return ext
}

func shouldVisualCompressSource(contentType string, ext string, size int64) bool {
	if size < largePageVisualCompressThreshold {
		return false
	}
	switch normalizeContentType(contentType) {
	case "image/jpeg":
		return true
	}
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

func imageLongEdge(width, height int) int {
	if width > height {
		return width
	}
	return height
}

func shouldVisualCompressDimensions(width, height int) bool {
	return width > 0 && height > 0 && imageLongEdge(width, height) > largePageVisualCompressMaxLongEdge
}

func (s *Server) pageJPEGDimensions(ctx context.Context, backend backendpkg.Backend, page apppkg.PageRef) (int, int, error) {
	switch page.SourceType {
	case "file":
		rc, _, _, err := backend.OpenStream(ctx, page.SourceRef)
		if err != nil {
			return 0, 0, err
		}
		defer rc.Close()
		cfg, err := jpeg.DecodeConfig(rc)
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	case "archive":
		archive, err := archivepkg.Open(ctx, backend, page.SourceRef, s.app.Config().Server.CacheDir)
		if err != nil {
			return 0, 0, err
		}
		defer archive.Close()
		rc, err := archive.Open(ctx, page.EntryName)
		if err != nil {
			return 0, 0, err
		}
		defer rc.Close()
		cfg, err := jpeg.DecodeConfig(rc)
		if err != nil {
			return 0, 0, err
		}
		return cfg.Width, cfg.Height, nil
	default:
		return 0, 0, os.ErrInvalid
	}
}

func (s *Server) shouldVisualCompressPage(ctx context.Context, backend backendpkg.Backend, page apppkg.PageRef, contentType string, ext string, size int64) (bool, int, int, error) {
	if !shouldVisualCompressSource(contentType, ext, size) {
		return false, 0, 0, nil
	}
	width, height, err := s.pageJPEGDimensions(ctx, backend, page)
	if err != nil {
		return false, 0, 0, err
	}
	return shouldVisualCompressDimensions(width, height), width, height, nil
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

func recompressVisualJPEG(data []byte, quality int) ([]byte, int, int, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()
	resized := resizeImageLongEdgeBilinear(img, largePageVisualCompressMaxLongEdge)
	resizedBounds := resized.Bounds()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flattenImageOnWhite(resized), &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, 0, 0, err
	}
	return buf.Bytes(), origWidth, origHeight, resizedBounds.Dx(), resizedBounds.Dy(), nil
}

func (s *Server) maybeCompressLargePageBytes(info ResolvedPageCacheInfo, page apppkg.PageRef, data []byte) ([]byte, bool, error) {
	if !info.VisualCompressed {
		return data, false, nil
	}
	optimized, origWidth, origHeight, resizedWidth, resizedHeight, err := recompressVisualJPEG(data, largePageVisualCompressQuality)
	if err != nil {
		return nil, false, err
	}
	s.log.Debugf("page visual compress ref=%s mode=%s before=%d after=%d quality=%d size=%dx%d->%dx%d", pageCacheLabel(page), info.Mode, len(data), len(optimized), largePageVisualCompressQuality, origWidth, origHeight, resizedWidth, resizedHeight)
	return optimized, true, nil
}

func (s *Server) WarmPageMemoryFromDiskCache(info ResolvedPageCacheInfo, page apppkg.PageRef) (bool, error) {
	if s.PageMemoryCache == nil {
		return false, nil
	}
	if _, ok := s.PageMemoryCache.Get(info.Key); ok {
		return false, nil
	}
	stat, err := os.Stat(info.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !s.PageMemoryCache.CanStore(stat.Size()) {
		return false, nil
	}
	data, err := os.ReadFile(info.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !s.PageMemoryCache.Add(info.Key, CachedPageBytes{Data: data, ContentType: info.ContentType, ModTime: info.ModTime}) {
		return false, nil
	}
	return true, nil
}

func pageMemoryWarmFlightKey(info ResolvedPageCacheInfo) string {
	return info.Key + ":memory-warm"
}

func (s *Server) schedulePageMemoryWarm(info ResolvedPageCacheInfo, page apppkg.PageRef) {
	if s.PageMemoryCache == nil {
		return
	}
	go func() {
		_, err, _ := s.DoPageFlight(pageMemoryWarmFlightKey(info), func() (bool, error) {
			return s.WarmPageMemoryFromDiskCache(info, page)
		})
		if err != nil {
			s.log.Debugf("page memory warm failed ref=%s err=%v", pageCacheLabel(page), err)
		}
	}()
}

func (s *Server) storePageCacheBytes(info ResolvedPageCacheInfo, page apppkg.PageRef, data []byte) (bool, error) {
	data, _, err := s.maybeCompressLargePageBytes(info, page, data)
	if err != nil {
		return false, err
	}
	storedInMemory := s.PageMemoryCache.Add(info.Key, CachedPageBytes{Data: data, ContentType: info.ContentType, ModTime: info.ModTime})
	created, err := s.writePageCacheBytes(info, data)
	if err != nil {
		if storedInMemory {
			s.log.Debugf("render cache persist deferred ref=%s err=%v", pageCacheLabel(page), err)
			s.schedulePageCachePersist(info, page, data)
			return false, nil
		}
		return false, err
	}
	return created, nil
}

func (s *Server) schedulePageCacheBuild(backend backendpkg.Backend, info ResolvedPageCacheInfo, page apppkg.PageRef) {
	go func() {
		if s.pageCacheBuildSem != nil {
			s.pageCacheBuildSem <- struct{}{}
			defer func() { <-s.pageCacheBuildSem }()
		}
		created, err, shared := s.DoPageFlight(info.Key, func() (bool, error) {
			return s.ensurePageCached(context.Background(), backend, page, info)
		})
		if err != nil {
			s.log.Debugf("render cache fill failed ref=%s err=%v", pageCacheLabel(page), err)
			return
		}
		if created && !shared {
			s.log.Debugf("render cache fill ref=%s", pageCacheLabel(page))
		}
	}()
}
