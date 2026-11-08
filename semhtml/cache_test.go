package semhtml

import "testing"

func TestBoundedCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newBoundedCache[int](2)
	cache.Store("first", 1)
	cache.Store("second", 2)
	if value, ok := cache.Load("first"); !ok || value != 1 {
		t.Fatalf("first cache value = %d, %v", value, ok)
	}
	cache.Store("third", 3)
	if _, ok := cache.Load("second"); ok {
		t.Fatal("least-recently-used entry was retained")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache length = %d, want 2", cache.Len())
	}
}
