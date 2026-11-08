package latexpdf

import (
	"container/list"
	"sync"
)

// boundedCache is a concurrency-safe LRU used for renderer intermediates. The
// caches are accelerators only: eviction never changes rendered output.
type boundedCache[V any] struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*list.Element
	order   list.List
}

type boundedCacheEntry[V any] struct {
	key   string
	value V
}

func newBoundedCache[V any](limit int) *boundedCache[V] {
	if limit < 1 {
		limit = 1
	}
	return &boundedCache[V]{limit: limit, entries: make(map[string]*list.Element)}
}

func (cache *boundedCache[V]) Load(key string) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	cache.order.MoveToFront(element)
	return element.Value.(boundedCacheEntry[V]).value, true
}

func (cache *boundedCache[V]) Store(key string, value V) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.entries[key]; ok {
		element.Value = boundedCacheEntry[V]{key: key, value: value}
		cache.order.MoveToFront(element)
		return
	}
	element := cache.order.PushFront(boundedCacheEntry[V]{key: key, value: value})
	cache.entries[key] = element
	for cache.order.Len() > cache.limit {
		oldest := cache.order.Back()
		delete(cache.entries, oldest.Value.(boundedCacheEntry[V]).key)
		cache.order.Remove(oldest)
	}
}

func (cache *boundedCache[V]) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.order.Len()
}
