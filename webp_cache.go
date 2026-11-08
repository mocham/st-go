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
	"time"

	"st-go/limits"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

const (
	staticWebPFrame       = -1
	webPCacheMaxEntries   = 4096
	webPCacheMaxByteUsage = limits.PreviewCacheBytes
)

// webPCache stores decoded WebP pixels as PNG. Animated frames use the hash of
// the source WebP plus their frame index and are cached only when requested.
type webPCache struct {
	db *sql.DB

	mu            sync.RWMutex
	closed        bool
	writes        chan webPCacheWrite
	touches       chan webPFrameKey
	stop          chan struct{}
	pendingFrames map[webPFrameKey]pendingWebPFrame
	pendingBytes  int
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
	// New cache databases use incremental auto-vacuum. Existing databases still
	// reuse pages freed by pruning even if their historical mode cannot change
	// without a full VACUUM.
	_, _ = db.Exec(`PRAGMA auto_vacuum=INCREMENTAL`)
	for _, schema := range []string{
		`CREATE TABLE IF NOT EXISTS webp_frames (
			source_hash BLOB NOT NULL,
			frame_index INTEGER NOT NULL,
			png BLOB NOT NULL,
			accessed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_hash, frame_index)
		) WITHOUT ROWID`,
	} {
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := ensureWebPCacheAccessColumn(db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS webp_frames_accessed_at ON webp_frames(accessed_at)`); err != nil {
		db.Close()
		return nil, err
	}
	cache := &webPCache{
		db:            db,
		writes:        make(chan webPCacheWrite, 2),
		touches:       make(chan webPFrameKey, 64),
		stop:          make(chan struct{}),
		pendingFrames: make(map[webPFrameKey]pendingWebPFrame),
	}
	cache.prune(webPCacheMaxEntries, webPCacheMaxByteUsage)
	go cache.runWriter()
	return cache, nil
}

func ensureWebPCacheAccessColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(webp_frames)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "accessed_at" {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE webp_frames ADD COLUMN accessed_at INTEGER NOT NULL DEFAULT 0`)
	return err
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
	c.pendingBytes = 0
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
	var encodedSize int64
	if err := c.db.QueryRow(
		`SELECT CASE WHEN length(png) <= ? THEN png END, length(png)
		 FROM webp_frames WHERE source_hash = ? AND frame_index = ?`,
		limits.FileBytes, hash[:], frameIdx,
	).Scan(&encoded, &encodedSize); err != nil {
		return 0, 0, nil, false
	}
	if !limits.Check("cached PNG", encodedSize, limits.FileBytes) {
		return 0, 0, nil, false
	}
	w, h, rgba, ok = decodeCachedPNG(encoded)
	if ok {
		c.touch(key)
	}
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
		`INSERT OR REPLACE INTO webp_frames (source_hash, frame_index, png, accessed_at) VALUES (?, ?, ?, ?)`,
		hash[:], frameIdx, encoded, time.Now().UnixNano(),
	)
	if err == nil {
		c.prune(webPCacheMaxEntries, webPCacheMaxByteUsage)
	}
	return err == nil
}

func (c *webPCache) touch(key webPFrameKey) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return
	}
	select {
	case c.touches <- key:
	default:
	}
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
	if !limits.Check("WebP cache pending pixels", int64(c.pendingBytes)+int64(len(rgba)), limits.MaxBytes) {
		return
	}
	c.pendingFrames[key] = pendingWebPFrame{w: w, h: h, rgba: rgba}
	c.pendingBytes += len(rgba)
	select {
	case c.writes <- job:
	default:
		delete(c.pendingFrames, key)
		c.pendingBytes -= len(rgba)
	}
}

func (c *webPCache) runWriter() {
	for {
		select {
		case <-c.stop:
			return
		case job := <-c.writes:
			c.putFrameHash(job.hash, job.frameIndex, job.w, job.h, job.rgba)
			c.mu.Lock()
			key := webPFrameKey{hash: job.hash, index: job.frameIndex}
			if frame, found := c.pendingFrames[key]; found {
				c.pendingBytes -= len(frame.rgba)
				delete(c.pendingFrames, key)
			}
			c.mu.Unlock()
		case key := <-c.touches:
			_, _ = c.db.Exec(
				`UPDATE webp_frames SET accessed_at=? WHERE source_hash=? AND frame_index=?`,
				time.Now().UnixNano(), key.hash[:], key.index,
			)
		}
	}
}

func (c *webPCache) prune(maxEntries, maxBytes int) {
	if c == nil || c.db == nil || maxEntries < 1 || maxBytes < 1 {
		return
	}
	var count int
	var total int64
	if err := c.db.QueryRow(`SELECT count(*), COALESCE(sum(length(png)), 0) FROM webp_frames`).Scan(&count, &total); err != nil {
		return
	}
	if count <= maxEntries && total <= int64(maxBytes) {
		return
	}
	type cachedKey struct {
		hash  []byte
		index int
		size  int64
	}
	rows, err := c.db.Query(`
		SELECT source_hash, frame_index, length(png)
		FROM webp_frames ORDER BY accessed_at, source_hash, frame_index`)
	if err != nil {
		return
	}
	var remove []cachedKey
	for rows.Next() && (count > maxEntries || total > int64(maxBytes)) {
		var key cachedKey
		if err := rows.Scan(&key.hash, &key.index, &key.size); err != nil {
			break
		}
		key.hash = append([]byte(nil), key.hash...)
		remove = append(remove, key)
		count--
		total -= key.size
	}
	_ = rows.Close()
	if len(remove) == 0 {
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		return
	}
	for _, key := range remove {
		if _, err := tx.Exec(`DELETE FROM webp_frames WHERE source_hash=? AND frame_index=?`, key.hash, key.index); err != nil {
			_ = tx.Rollback()
			return
		}
	}
	if tx.Commit() == nil {
		_, _ = c.db.Exec(`PRAGMA incremental_vacuum(64)`)
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
	buf := limits.Buffer{Scope: "encoded cache PNG"}
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func decodeCachedPNG(encoded []byte) (w, h int, rgba []byte, ok bool) {
	if !limits.Check("cached PNG", int64(len(encoded)), limits.FileBytes) {
		return 0, 0, nil, false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(encoded))
	if err != nil {
		return 0, 0, nil, false
	}
	// PNG may decode to 16-bit channels before conversion to RGBA8.
	if _, valid := limits.Product("cached PNG pixels", limits.PixelBytes, cfg.Width, cfg.Height, 8); !valid {
		return 0, 0, nil, false
	}
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
	size, ok := limits.RGBA("cached pixels", w, h)
	if !ok {
		return 0
	}
	return size
}

func validRGBA(w, h int, rgba []byte) bool {
	size := rgbaSize(w, h)
	return size > 0 && len(rgba) == size
}
