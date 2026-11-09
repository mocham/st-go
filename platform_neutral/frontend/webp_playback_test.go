//go:build webpcache

package frontend

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"st-go/term"
)

func TestWebPAnimationPlaybackAcrossCacheStates(t *testing.T) {
	data := makeAnimWebp(t)
	if data == nil {
		t.Skip("ffmpeg not available")
	}
	durations, _, _, ok := decodeWebPAnimInfo(data)
	if !ok || len(durations) < 3 {
		t.Fatalf("animation metadata has %d frames, ok=%v", len(durations), ok)
	}
	for _, test := range []struct {
		name         string
		cachedFrames []int
		migrated     bool
	}{
		{name: "empty"},
		{name: "frame-0", cachedFrames: []int{0}},
		{name: "frame-1", cachedFrames: []int{1}},
		{name: "frames-0-1", cachedFrames: []int{0, 1}},
		{name: "obsolete-schema", cachedFrames: []int{0}, migrated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cachePath := filepath.Join(t.TempDir(), "frames.sqlite3")
			if test.migrated {
				db, err := sql.Open("sqlite3", cachePath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TABLE webp_frames (
					source_hash BLOB NOT NULL, frame_index INTEGER NOT NULL,
					png BLOB NOT NULL, accessed_at INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY (source_hash, frame_index)) WITHOUT ROWID;
					CREATE INDEX webp_frames_accessed_at ON webp_frames(accessed_at);`)
				closeErr := db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			cache, err := openWebPCache(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			defer cache.close()
			for _, index := range test.cachedFrames {
				w, h, rgba, decoded := decodeWebPAnimFrame(data, index)
				if !decoded || !cache.putFrame(data, index, w, h, rgba) {
					t.Fatalf("could not prefill frame %d", index)
				}
			}

			frontend := testImageTerminal(20, 10, 8, 16)
			frontend.webpCache = cache
			if !ensureFramebuffer(frontend.cols*frontend.cw, frontend.rows*frontend.ch) {
				t.Fatal("could not allocate playback framebuffer")
			}
			frontend.clearFramebuffer()
			opts := term.ImageDecodeOptions{FitContain: true, ViewCols: 10, ViewRows: 7}
			decodedDurations, _, cols, rows, decoded := frontend.ImageDecodeAnim(data, opts)
			if !decoded {
				t.Fatal("animation metadata did not decode through frontend")
			}
			core := frontend.termCore
			frontend.attachTerm(core)
			core.SetAnim(data, opts, decodedDurations, cols, rows, 0, 0, 10, 8)
			if !core.HasAnim() {
				t.Fatal("animation stopped while drawing frame 0")
			}
			previous := append([]uint32(nil), framebuf...)
			start := time.Now()
			elapsed := 0
			for index := 1; index < min(3, len(decodedDurations)); index++ {
				elapsed += decodedDurations[index-1]
				if !core.TickAnim(start.Add(time.Duration(elapsed+1) * time.Millisecond)) {
					t.Fatalf("animation stopped before frame %d", index)
				}
				if slices.Equal(previous, framebuf) {
					t.Fatalf("frame %d advanced without changing framebuffer pixels", index)
				}
				previous = append(previous[:0], framebuf...)
			}
		})
	}
}
