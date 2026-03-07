package httpapi

import (
	"container/list"
	"sync"
	"time"
)

type CachedPageBytes struct {
	Data        []byte
	ContentType string
	ModTime     time.Time
}

type PageMemoryCache struct {
	mu       sync.Mutex
	maxBytes int64
	size     int64
	items    map[string]*list.Element
	order    *list.List
}

type pageMemoryCacheEntry struct {
	key   string
	value CachedPageBytes
	size  int64
}

func NewPageMemoryCache(maxBytes int64) *PageMemoryCache {
	if maxBytes <= 0 {
		return nil
	}
	return &PageMemoryCache{
		maxBytes: maxBytes,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *PageMemoryCache) MaxBytes() int64 {
	if c == nil {
		return 0
	}
	return c.maxBytes
}

func (c *PageMemoryCache) CanStore(size int64) bool {
	return c != nil && size > 0 && size <= c.maxBytes
}

func (c *PageMemoryCache) Get(key string) (CachedPageBytes, bool) {
	if c == nil {
		return CachedPageBytes{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return CachedPageBytes{}, false
	}
	c.order.MoveToFront(elem)
	entry := elem.Value.(*pageMemoryCacheEntry)
	return entry.value, true
}

func (c *PageMemoryCache) Add(key string, value CachedPageBytes) bool {
	if c == nil {
		return false
	}
	size := int64(len(value.Data))
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

func (c *PageMemoryCache) trim() {
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
