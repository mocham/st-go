//go:build linux && ffmpeg_static && cgo && !purego

package fb

import (
	"encoding/binary"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const recordingTestsUseExternalEncoder = false

// This smoke test also runs on hosts with neither ffmpeg nor ffprobe installed.
// The shared MP4 tests additionally decode and inspect the result when available.
func TestStaticRecordingWithoutExternalTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "record.mp4")
	stop := make(chan struct{})
	close(stop)
	if err := encodeScreenRecording(path, 3, 1, stop, func(frame *image.RGBA) {
		for at := 0; at < len(frame.Pix); at += 4 {
			frame.Pix[at], frame.Pix[at+3] = 255, 255
		}
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boxes := make(map[string]bool)
	for len(data) >= 8 {
		size := uint64(binary.BigEndian.Uint32(data[:4]))
		kind := string(data[4:8])
		if size == 1 && len(data) >= 16 {
			size = binary.BigEndian.Uint64(data[8:16])
		}
		if size < 8 || size > uint64(len(data)) {
			t.Fatalf("invalid MP4 box %q size %d", kind, size)
		}
		boxes[kind] = true
		data = data[size:]
	}
	if len(data) != 0 || !boxes["ftyp"] || !boxes["mdat"] || !boxes["moov"] {
		t.Fatal("recording is missing its MP4 header, video, or finalized index", boxes)
	}
}

func TestStaticRecordingReportsOutputFailure(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full unavailable")
	}
	stop := make(chan struct{})
	close(stop)
	if err := encodeRecordingFile("/dev/full", 16, 16, stop, func(*image.RGBA) {}); err == nil {
		t.Fatal("native encoder ignored a failed output write")
	}
}

func TestStaticRecordingPreservesTimeAcrossMissedFrames(t *testing.T) {
	_, probe := requireRecordingEncoder(t)
	path := filepath.Join(t.TempDir(), "record.mp4")
	stop := make(chan struct{})
	frames := 0
	var first, last time.Time
	err := encodeScreenRecording(path, 64, 48, stop, func(*image.RGBA) {
		frames++
		if frames == 1 {
			first = time.Now()
		}
		if frames == 3 {
			// Simulate a busy compositor rather than manufacturing timestamps.
			time.Sleep(250 * time.Millisecond)
		}
		if frames == 6 {
			last = time.Now()
			close(stop)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	want := last.Sub(first).Seconds() + 1.0/recordingFPS
	if err != nil || math.Abs(duration-want) > 0.1 {
		t.Fatalf("video duration %.3fs, wall-clock duration %.3fs: %v", duration, want, err)
	}
}
