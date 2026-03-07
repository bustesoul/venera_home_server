package main

import (
	"bytes"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
)

const pagePrefetchThrottleStep = 4
const largePageVisualCompressThreshold = 3 * 1024 * 1024
const largePageVisualCompressQuality = 90
const largePageVisualCompressMinSavings = 256 * 1024

func prefetchThrottleWindowStart(windowStart int) int {
	if windowStart < 0 {
		return 0
	}
	if pagePrefetchThrottleStep <= 1 {
		return windowStart
	}
	return (windowStart / pagePrefetchThrottleStep) * pagePrefetchThrottleStep
}

func shouldVisualCompressPageSize(info resolvedPageCacheInfo, size int64) bool {
	if size < largePageVisualCompressThreshold {
		return false
	}
	if strings.EqualFold(info.contentType, "image/jpeg") {
		return true
	}
	switch strings.ToLower(filepath.Ext(info.path)) {
	case ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

func recompressJPEG(data []byte, quality int) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *apiServer) maybeCompressLargePageBytes(info resolvedPageCacheInfo, page PageRef, data []byte) ([]byte, bool) {
	if !shouldVisualCompressPageSize(info, int64(len(data))) {
		return data, false
	}
	optimized, err := recompressJPEG(data, largePageVisualCompressQuality)
	if err != nil {
		return data, false
	}
	if len(optimized) >= len(data)-largePageVisualCompressMinSavings {
		return data, false
	}
	s.log.Printf("page visual compress ref=%s before=%d after=%d quality=%d", pageCacheLabel(page), len(data), len(optimized), largePageVisualCompressQuality)
	return optimized, true
}

func (s *apiServer) warmPageMemoryFromDiskCache(info resolvedPageCacheInfo, page PageRef) (bool, error) {
	if s.pageMemoryCache == nil {
		return false, nil
	}
	if _, ok := s.pageMemoryCache.Get(info.key); ok {
		return false, nil
	}
	stat, err := os.Stat(info.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !s.pageMemoryCache.CanStore(stat.Size()) && !shouldVisualCompressPageSize(info, stat.Size()) {
		return false, nil
	}
	data, err := os.ReadFile(info.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	data, _ = s.maybeCompressLargePageBytes(info, page, data)
	if !s.pageMemoryCache.CanStore(int64(len(data))) {
		return false, nil
	}
	if !s.pageMemoryCache.Add(info.key, cachedPageBytes{data: data, contentType: info.contentType, modTime: info.modTime}) {
		return false, nil
	}
	return true, nil
}


func pageMemoryWarmFlightKey(info resolvedPageCacheInfo) string {
	return info.key + ":memory-warm"
}

func (s *apiServer) schedulePageMemoryWarm(info resolvedPageCacheInfo, page PageRef) {
	if s.pageMemoryCache == nil {
		return
	}
	go func() {
		_, err, _ := s.doPageFlight(pageMemoryWarmFlightKey(info), func() (bool, error) {
			return s.warmPageMemoryFromDiskCache(info, page)
		})
		if err != nil {
			s.log.Printf("page memory warm failed ref=%s err=%v", pageCacheLabel(page), err)
		}
	}()
}

func (s *apiServer) storePageCacheBytes(info resolvedPageCacheInfo, page PageRef, data []byte) (bool, error) {
	data, _ = s.maybeCompressLargePageBytes(info, page, data)
	storedInMemory := s.pageMemoryCache.Add(info.key, cachedPageBytes{data: data, contentType: info.contentType, modTime: info.modTime})
	created, err := s.writePageCacheBytes(info, data)
	if err != nil {
		if storedInMemory {
			s.log.Printf("render cache persist deferred ref=%s err=%v", pageCacheLabel(page), err)
			s.schedulePageCachePersist(info, page, data)
			return false, nil
		}
		return false, err
	}
	return created, nil
}
