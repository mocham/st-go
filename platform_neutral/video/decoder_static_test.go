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
			pcm, err := d.audio(48000)
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

func TestNativeAudioDurationFollowsPlaybackSpeed(t *testing.T) {
	path := testMovie(t)
	for _, speed := range []float64{0.5, 2} {
		t.Run(fmt.Sprint(speed), func(t *testing.T) {
			d, err := openNative(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.close()
			samples := 0
			for {
				kind, _, err := d.next()
				if err != nil {
					t.Fatal(err)
				}
				if kind == 0 {
					break
				}
				if kind != 2 {
					continue
				}
				pcm, err := d.audio(int(48000 / speed))
				if err != nil {
					t.Fatal(err)
				}
				samples += len(pcm) / 4
			}
			seconds := float64(samples) / 48000
			if math.Abs(seconds-1.6/speed) > 0.05 {
				t.Fatalf("audio duration %.3f at speed %.2f", seconds, speed)
			}
		})
	}
}
