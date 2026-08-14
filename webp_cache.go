//go:build webpcache

package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

const staticWebPFrame = -1

// webPCache stores decoded WebP pixels as PNG. Animated frames use the hash of
// the source WebP plus their frame index and are cached only when requested.
type webPCache struct {
	db *sql.DB

	mu            sync.RWMutex
	closed        bool
	writes        chan webPCacheWrite
	stop          chan struct{}
	pendingFrames map[webPFrameKey]pendingWebPFrame
}

type webPFrameKey struct {
	hash  [sha256.Size]byte
	index int
}

type pendingWebPFrame struct {
	w, h int
	rgba []byte
}

type webPCacheWrite struct {
	hash       [sha256.Size]byte
	frameIndex int
	w, h       int
	rgba       []byte
}

func openWebPCache(path string) (*webPCache, error) {
	if path == "" {
		return nil, nil
	}
	if err := secureCacheFile(path); err != nil {
		return nil, err
	}
	params := url.Values{
		"_busy_timeout": []string{"100"},
		"_journal_mode": []string{"WAL"},
		"_synchronous":  []string{"NORMAL"},
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// The writer never holds a transaction while encoding. A second connection
	// keeps foreground cache reads from waiting on its short INSERT statements.
	db.SetMaxOpenConns(2)
	for _, schema := range []string{
		`CREATE TABLE IF NOT EXISTS webp_frames (
			source_hash BLOB NOT NULL,
			frame_index INTEGER NOT NULL,
			png BLOB NOT NULL,
			PRIMARY KEY (source_hash, frame_index)
		) WITHOUT ROWID`,
	} {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, err
		}
	}
	cache := &webPCache{
		db:            db,
		writes:        make(chan webPCacheWrite, 2),
		stop:          make(chan struct{}),
		pendingFrames: make(map[webPFrameKey]pendingWebPFrame),
	}
	go cache.runWriter()
	return cache, nil
}

func secureCacheFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var dirStat unix.Stat_t
	if err := unix.Lstat(dir, &dirStat); err != nil {
		return err
	}
	if dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || dirStat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("cache directory %s is not owned by the current user", dir)
	}
	if dirStat.Mode&0022 != 0 {
		return fmt.Errorf("cache directory %s is writable by another user", dir)
	}

	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%s is not owned by the current user", path)
	}
	return unix.Fchmod(fd, 0600)
}

func (c *webPCache) close() {
	if c == nil || c.db == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.stop)
	c.pendingFrames = nil
	c.mu.Unlock()
	c.db.Close()
}

func webPHash(data []byte) [sha256.Size]byte {
	return sha256.Sum256(data)
}

func (c *webPCache) frame(data []byte, frameIdx int) (w, h int, rgba []byte, ok bool) {
	if c == nil || c.db == nil {
		return 0, 0, nil, false
	}
	hash := webPHash(data)
	key := webPFrameKey{hash: hash, index: frameIdx}
	c.mu.RLock()
	if frame, found := c.pendingFrames[key]; found {
		c.mu.RUnlock()
		return frame.w, frame.h, frame.rgba, true
	}
	c.mu.RUnlock()

	var encoded []byte
	if err := c.db.QueryRow(
		`SELECT png FROM webp_frames WHERE source_hash = ? AND frame_index = ?`,
		hash[:], frameIdx,
	).Scan(&encoded); err != nil {
		return 0, 0, nil, false
	}
	w, h, rgba, ok = decodeCachedPNG(encoded)
	return w, h, rgba, ok
}

func (c *webPCache) putFrame(data []byte, frameIdx, w, h int, rgba []byte) bool {
	if c == nil || c.db == nil {
		return false
	}
	return c.putFrameHash(webPHash(data), frameIdx, w, h, rgba)
}

func (c *webPCache) putFrameHash(hash [sha256.Size]byte, frameIdx, w, h int, rgba []byte) bool {
	encoded, ok := encodeCachedPNG(w, h, rgba)
	if !ok {
		return false
	}
	_, err := c.db.Exec(
		`INSERT OR REPLACE INTO webp_frames (source_hash, frame_index, png) VALUES (?, ?, ?)`,
		hash[:], frameIdx, encoded,
	)
	return err == nil
}

func (c *webPCache) putFrameAsync(data []byte, frameIdx, w, h int, rgba []byte) {
	if c == nil || c.db == nil || !validRGBA(w, h, rgba) {
		return
	}
	hash := webPHash(data)
	key := webPFrameKey{hash: hash, index: frameIdx}
	job := webPCacheWrite{hash: hash, frameIndex: frameIdx, w: w, h: h, rgba: rgba}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, found := c.pendingFrames[key]; found {
		return
	}
	c.pendingFrames[key] = pendingWebPFrame{w: w, h: h, rgba: rgba}
	select {
	case c.writes <- job:
	default:
		delete(c.pendingFrames, key)
	}
}

func (c *webPCache) runWriter() {
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		select {
		case <-c.stop:
			return
		case job := <-c.writes:
			select {
			case <-c.stop:
				return
			default:
			}
			c.putFrameHash(job.hash, job.frameIndex, job.w, job.h, job.rgba)
			c.mu.Lock()
			delete(c.pendingFrames, webPFrameKey{hash: job.hash, index: job.frameIndex})
			c.mu.Unlock()
		}
	}
}

func encodeCachedPNG(w, h int, rgba []byte) ([]byte, bool) {
	if !validRGBA(w, h, rgba) {
		return nil, false
	}
	img := &image.NRGBA{
		Pix:    rgba,
		Stride: w * 4,
		Rect:   image.Rect(0, 0, w, h),
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func decodeCachedPNG(encoded []byte) (w, h int, rgba []byte, ok bool) {
	img, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, nil, false
	}
	bounds := img.Bounds()
	w, h = bounds.Dx(), bounds.Dy()
	if rgbaSize(w, h) == 0 {
		return 0, 0, nil, false
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	return w, h, dst.Pix, true
}

func rgbaSize(w, h int) int {
	if w <= 0 || h <= 0 || w > int(^uint(0)>>1)/h/4 {
		return 0
	}
	return w * h * 4
}

func validRGBA(w, h int, rgba []byte) bool {
	size := rgbaSize(w, h)
	return size > 0 && len(rgba) == size
}
