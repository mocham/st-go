//go:build linux && ffmpeg_static && cgo && !purego

package video

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func testMovie(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is needed only to generate the test fixture")
	}
	path := filepath.Join(t.TempDir(), "movie ';\n世界.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=128x72:rate=20:duration=1.6", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1.6", "-c:v", "libx264", "-threads", "1", "-bf", "2", "-g", "10", "-pix_fmt", "yuv420p", "-c:a", "aac", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate movie: %v: %s", err, out)
	}
	// The decoder and player must not need any external commands.
	t.Setenv("PATH", t.TempDir())
	return path
}

func TestNativeDecodeAudioVideoAndSeekWithoutExternalTools(t *testing.T) {
	path := testMovie(t)
	d, err := openNative(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	m := d.info()
	if m.width != 128 || m.height != 72 || !m.audio || m.duration < 1500*time.Millisecond {
		t.Fatal(m)
	}
	frames, audioFrames, samples := 0, 0, 0
	last := time.Duration(-1)
	nonzero := false
	for {
		kind, pts, err := d.next()
		if err != nil {
			t.Fatal(err)
		}
		if kind == 0 {
			tail, err := d.audio(48000, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			samples += len(tail) / 4
			break
		}
		if kind == 1 {
			frames++
			if pts < last {
				t.Fatalf("video timestamps went backwards: %v < %v", pts, last)
			}
			last = pts
			pixels, err := d.pixels(160, 120)
			if err != nil {
				t.Fatal(err)
			}
			if len(pixels) != 160*120 || pixels[0] != 0xff000000 || pixels[60*160+80] == 0xff000000 {
				t.Fatal("missing video or incorrect letterbox")
			}
		} else {
			audioFrames++
			pcm, err := d.audio(48000, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			samples += len(pcm) / 4
			for i := 0; i+1 < len(pcm); i += 2 {
				if binary.LittleEndian.Uint16(pcm[i:]) != 0 {
					nonzero = true
				}
			}
		}
	}
	if frames != 32 || audioFrames < 60 || samples < 75000 || samples > 80000 || !nonzero {
		t.Fatalf("video=%d audio=%d samples=%d nonzero=%v", frames, audioFrames, samples, nonzero)
	}
	// Seek after EOF must flush both decoders, including reordered H.264 frames.
	if err := d.seek(900 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 120; i++ {
		k, pts, err := d.next()
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 {
			break
		}
		if k == 1 && pts >= 900*time.Millisecond {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("seek failed to produce the requested frame")
	}
}

func TestNativeRejectsInvalidInputAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.mp4")
	if err := os.WriteFile(path, []byte("invalid movie"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, filepath.Dir(path), "/dev/zero", path + "missing"} {
		if d, err := openNative(context.Background(), name); err == nil {
			d.close()
			t.Fatalf("accepted %q", name)
		}
	}
	movie := testMovie(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d, err := openNative(ctx, movie); err == nil {
		defer d.close()
		// Cancellation can race the small probe, but must stop decoding.
		select {
		case <-d.(*nativeDecoder).joined:
		case <-time.After(time.Second):
			t.Fatal("cancel monitor stuck")
		}
		if _, _, err := d.next(); err == nil {
			t.Fatal("decoder ignored cancellation")
		}
	}
}

func awaitPlayer(t *testing.T, p *Player, condition func(api.VideoStatus) bool) api.VideoStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := p.Status()
		if s.State == "error" {
			t.Fatal(s.Error)
		}
		if condition(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("player did not reach expected state: %+v", p.Status())
	return api.VideoStatus{}
}

func TestPlayerPauseSeekResizeReplayAndClose(t *testing.T) {
	path := testMovie(t)
	var mu sync.Mutex
	frames, w, h := 0, 0, 0
	p := Start(path, 160, 120, 100, true, func(pixels []uint32, width, height int) {
		mu.Lock()
		defer mu.Unlock()
		frames++
		w, h = width, height
		if len(pixels) != w*h {
			t.Error("incorrect frame dimensions")
		}
	})
	defer func() {
		p.Close()
		select {
		case <-p.Done():
		case <-time.After(time.Second):
			t.Error("player did not close")
		}
	}()
	control := func(r api.VideoRequest) {
		t.Helper()
		if err := p.Control(r); err != nil {
			t.Fatal(err)
		}
	}
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.PositionMS >= 150 })
	control(api.VideoRequest{Action: "toggle"})
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "paused" })
	mu.Lock()
	before := frames
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	after := frames
	mu.Unlock()
	if before != after {
		t.Fatal("frames advanced while paused")
	}
	control(api.VideoRequest{Action: "speed", Speed: 2})
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "paused" && s.Speed == 2 })
	control(api.VideoRequest{Action: "seek", PositionMS: 900})
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "paused" && s.PositionMS >= 900 })
	p.Resize(100, 80)
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		resized := w == 100 && h == 80
		mu.Unlock()
		if resized {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused resize was not repainted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	control(api.VideoRequest{Action: "toggle"})
	resumed := time.Now()
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "ended" })
	if elapsed := time.Since(resumed); elapsed > 600*time.Millisecond {
		t.Fatalf("2x playback took %v for 700ms of media", elapsed)
	}
	mu.Lock()
	before = frames
	mu.Unlock()
	control(api.VideoRequest{Action: "seek", PositionMS: p.Status().DurationMS})
	awaitPlayer(t, p, func(s api.VideoStatus) bool {
		mu.Lock()
		defer mu.Unlock()
		return frames > before && s.State == "ended" && s.PositionMS == s.DurationMS
	})
	control(api.VideoRequest{Action: "toggle"})
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "playing" && s.PositionMS < 900 })
}

// A fixed 440 Hz fixture catches pitch shifts even if the output duration is
// correct. Inspect the actual decoded/time-stretched PCM, without a sound card.
func toneFrequency(t *testing.T, pcm []byte) float64 {
	t.Helper()
	frames := len(pcm) / 4
	if frames < 2000 {
		t.Fatalf("insufficient audio: %d frames", frames)
	}
	start, end := frames/10, frames*9/10
	previous := int16(binary.LittleEndian.Uint16(pcm[(start-1)*4:]))
	var first, last, energy float64
	crossings := 0
	for i := start; i < end; i++ {
		sample := int16(binary.LittleEndian.Uint16(pcm[i*4:]))
		energy += float64(sample) * float64(sample)
		if previous <= 0 && sample > 0 {
			crossing := float64(i-1) - float64(previous)/float64(int(sample)-int(previous))
			if crossings == 0 {
				first = crossing
			}
			last = crossing
			crossings++
		}
		previous = sample
	}
	if crossings < 10 || energy/float64(end-start) < 10000 {
		t.Fatal("missing audible test tone")
	}
	return float64(crossings-1) * 48000 / (last - first)
}

func decodedTempoAudio(t *testing.T, d decoder, speedAt func(time.Duration) float64) []byte {
	t.Helper()
	var samples []byte
	speed := 1.0
	for {
		kind, pts, err := d.next()
		if err != nil {
			t.Fatal(err)
		}
		if kind != 0 && kind != 2 {
			continue
		}
		if kind == 2 {
			speed = speedAt(pts)
		}
		pcm, err := d.audio(48000, speed, kind == 0)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, pcm...)
		if kind == 0 {
			return samples
		}
	}
}

func TestNativeAudioPreservesPitchAtEveryPlaybackSpeed(t *testing.T) {
	path := testMovie(t)
	for _, speed := range []float64{0.25, 0.5, 1, 1.1, 2, 4} {
		t.Run(fmt.Sprint(speed), func(t *testing.T) {
			d, err := openNative(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.close()
			pcm := decodedTempoAudio(t, d, func(time.Duration) float64 { return speed })
			seconds := float64(len(pcm)/4) / 48000
			if math.Abs(seconds-1.6/speed) > 0.12 {
				t.Fatalf("audio duration %.3f at speed %.2f", seconds, speed)
			}
			if hz := toneFrequency(t, pcm); math.Abs(hz-440) > 3 {
				t.Fatalf("pitch shifted to %.2f Hz at %.2fx", hz, speed)
			}
			// Draining again must not repeat the tail or feed a stale frame.
			tail, err := d.audio(48000, speed, true)
			if err != nil || len(tail) != 0 {
				t.Fatalf("second drain: %d bytes, %v", len(tail), err)
			}
		})
	}
}

func TestNativeAudioTempoChangesAndSeekReset(t *testing.T) {
	path := testMovie(t)
	d, err := openNative(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	pcm := decodedTempoAudio(t, d, func(pts time.Duration) float64 {
		switch {
		case pts < 400*time.Millisecond:
			return 0.5
		case pts < 800*time.Millisecond:
			return 1
		case pts < 1200*time.Millisecond:
			return 2
		default:
			return 4
		}
	})
	if hz := toneFrequency(t, pcm); math.Abs(hz-440) > 3 {
		t.Fatalf("pitch shifted during speed changes: %.2f Hz", hz)
	}
	if seconds := float64(len(pcm)/4) / 48000; math.Abs(seconds-1.5) > 0.15 {
		t.Fatalf("lost audio across speed changes: %.3fs", seconds)
	}
	// Normal adjacent samples of this low-frequency tone differ only slightly.
	// Recreating filters/discarding their history can introduce abrupt jumps.
	for i := len(pcm) / 40 * 4; i < len(pcm)*9/10-4; i += 4 {
		a := int16(binary.LittleEndian.Uint16(pcm[i:]))
		b := int16(binary.LittleEndian.Uint16(pcm[i+4:]))
		if math.Abs(float64(a)-float64(b)) > 1500 {
			t.Fatalf("discontinuity at output frame %d", i/4)
		}
	}
	if err := d.seek(0); err != nil {
		t.Fatal(err)
	}
	tail, err := d.audio(48000, 1, true)
	if err != nil || len(tail) != 0 {
		t.Fatalf("seek retained old audio: %d bytes, %v", len(tail), err)
	}
	pcm = decodedTempoAudio(t, d, func(time.Duration) float64 { return 1 })
	if hz := toneFrequency(t, pcm); math.Abs(hz-440) > 3 {
		t.Fatalf("bad pitch after seek: %.2f Hz", hz)
	}
	if seconds := float64(len(pcm)/4) / 48000; math.Abs(seconds-1.6) > 0.05 {
		t.Fatalf("bad duration after seek: %.3fs", seconds)
	}
}

func TestNativeAudioResetDiscardsFilterTail(t *testing.T) {
	path := testMovie(t)
	d, err := openNative(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	for i := 0; i < 30; i++ {
		kind, _, err := d.next()
		if err != nil {
			t.Fatal(err)
		}
		if kind == 2 {
			if _, err := d.audio(48000, 0.25, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.resetAudio()
	tail, err := d.audio(48000, 0.25, true)
	if err != nil || len(tail) != 0 {
		t.Fatalf("reset retained old audio: %d bytes, %v", len(tail), err)
	}
}
