package main

import (
	"container/list"
	"sync"
	"time"
)

type cachedPageBytes struct {
	data        []byte
	contentType string
	modTime     time.Time
}

type pageMemoryCache struct {
	mu       sync.Mutex
	maxBytes int64
	size     int64
	items    map[string]*list.Element
	order    *list.List
}

type pageMemoryCacheEntry struct {
	key   string
	value cachedPageBytes
	size  int64
}

func newPageMemoryCache(maxBytes int64) *pageMemoryCache {
	if maxBytes <= 0 {
		return nil
	}
	return &pageMemoryCache{
		maxBytes: maxBytes,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *pageMemoryCache) MaxBytes() int64 {
	if c == nil {
		return 0
	}
	return c.maxBytes
}

func (c *pageMemoryCache) CanStore(size int64) bool {
	return c != nil && size > 0 && size <= c.maxBytes
}

func (c *pageMemoryCache) Get(key string) (cachedPageBytes, bool) {
	if c == nil {
		return cachedPageBytes{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return cachedPageBytes{}, false
	}
	c.order.MoveToFront(elem)
	entry := elem.Value.(*pageMemoryCacheEntry)
	return entry.value, true
}

func (c *pageMemoryCache) Add(key string, value cachedPageBytes) bool {
	if c == nil {
		return false
	}
	size := int64(len(value.data))
	if size <= 0 || size > c.maxBytes {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*pageMemoryCacheEntry)
		c.size -= entry.size
		entry.value = value
		entry.size = size
		c.size += size
		c.order.MoveToFront(elem)
		c.trim()
		return true
	}
	entry := &pageMemoryCacheEntry{key: key, value: value, size: size}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
	c.size += size
	c.trim()
	return true
}

func (c *pageMemoryCache) trim() {
	for c.size > c.maxBytes {
		elem := c.order.Back()
		if elem == nil {
			c.size = 0
			return
		}
		entry := elem.Value.(*pageMemoryCacheEntry)
		delete(c.items, entry.key)
		c.size -= entry.size
		c.order.Remove(elem)
	}
}
