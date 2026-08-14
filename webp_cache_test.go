//go:build webpcache

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWebPCacheFrameRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "webp?cache.sqlite3")
	cache, err := openWebPCache(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("cache permissions = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("cache directory permissions = %o, want 700", got)
	}

	source := []byte("webp source A")
	rgba := []byte{
		255, 0, 0, 255, 0, 255, 0, 255,
		0, 0, 255, 255, 255, 255, 255, 255,
	}
	if !cache.putFrame(source, staticWebPFrame, 2, 2, rgba) {
		t.Fatal("putFrame failed")
	}
	w, h, got, ok := cache.frame(source, staticWebPFrame)
	if !ok || w != 2 || h != 2 || !bytes.Equal(got, rgba) {
		t.Fatalf("frame = %dx%d %v ok=%v, want 2x2 %v", w, h, got, ok, rgba)
	}
	if _, _, _, ok := cache.frame([]byte("webp source B"), staticWebPFrame); ok {
		t.Fatal("different source hash returned a cached frame")
	}
	if _, _, _, ok := cache.frame(source, 0); ok {
		t.Fatal("different frame index returned a cached frame")
	}

	var encoded []byte
	if err := cache.db.QueryRow(`SELECT png FROM webp_frames`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encoded, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("cached frame is not PNG: %x", encoded[:min(len(encoded), 8)])
	}
}

func TestWebPCacheAsyncFrameWriteDoesNotBlock(t *testing.T) {
	cache, err := openWebPCache(filepath.Join(t.TempDir(), "webp.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()

	const width, height = 1024, 1024
	rgba := make([]byte, width*height*4)
	var value uint32 = 1
	for i := range rgba {
		value = value*1664525 + 1013904223
		rgba[i] = byte(value >> 24)
	}
	source := []byte("large webp source")
	started := time.Now()
	cache.putFrameAsync(source, staticWebPFrame, width, height, rgba)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("asynchronous cache enqueue blocked for %v", elapsed)
	}

	w, h, got, ok := cache.frame(source, staticWebPFrame)
	if !ok || w != width || h != height || !bytes.Equal(got, rgba) {
		t.Fatalf("pending frame = %dx%d ok=%v", w, h, ok)
	}
	waitForCachedRows(t, cache, "webp_frames", 1)
}

func waitForCachedRows(t *testing.T, cache *webPCache, table string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := cache.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err == nil && count == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rows in %s", want, table)
}

func TestOpenWebPCacheEmptyPathDisablesCache(t *testing.T) {
	cache, err := openWebPCache("")
	if err != nil || cache != nil {
		t.Fatalf("openWebPCache empty path = %#v, %v; want nil, nil", cache, err)
	}
}
