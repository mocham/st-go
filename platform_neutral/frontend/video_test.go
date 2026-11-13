package frontend

import (
	"image"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/platform_neutral/video"
)

type compositedVideoHost struct {
	api.Host
	surface *compositedVideoSurface
}

func (*compositedVideoHost) Close() {}
func (h *compositedVideoHost) NewSurface(r image.Rectangle, _ uint32) (api.Surface, error) {
	h.surface = &compositedVideoSurface{bounds: r}
	return h.surface, nil
}

type compositedVideoSurface struct {
	mu     sync.Mutex
	bounds image.Rectangle
	pixel  uint32
	closed bool
}

func (s *compositedVideoSurface) Configure(r image.Rectangle, _ uint32) {
	s.mu.Lock()
	s.bounds = r
	s.mu.Unlock()
}
func (s *compositedVideoSurface) Present(pixels []uint32, w, h, _ int, _ bool) {
	s.mu.Lock()
	s.pixel = pixels[h/2*w+w/2]
	s.mu.Unlock()
}
func (*compositedVideoSurface) Expose() bool { return true }
func (s *compositedVideoSurface) Close()     { s.mu.Lock(); s.closed = true; s.mu.Unlock() }

func TestCompositedVideoUsesSurfaceAndClosesWithAlternateScreen(t *testing.T) {
	if !video.Supported() {
		t.Skip("bundled video profile")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("fixture generator unavailable")
	}
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=red:size=80x24:rate=10:duration=2", "-c:v", "libx264", "-threads", "1", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	t.Setenv("PATH", t.TempDir())
	trm := regionTestTerminal(t)
	host := &compositedVideoHost{}
	trm.host = host
	trm.mu.Lock()
	trm.termCore.Twrite([]byte("\x1b[?1049h"), false)
	trm.Video("00112233445566778899aabbccddeeff", api.VideoRequest{Action: "open", File: path, Bounds: api.Rect{X: 1, Y: 2, Width: 20, Height: 3}, Volume: 100, Muted: true})
	v := trm.video
	trm.mu.Unlock()
	if v == nil || v.player == nil {
		t.Fatal("no player")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		host.surface.mu.Lock()
		pixel, bounds := host.surface.pixel, host.surface.bounds
		host.surface.mu.Unlock()
		if pixel>>16&255 > 200 && pixel&255 < 30 {
			if bounds != image.Rect(1, 9, 81, 33) {
				t.Fatal("incorrect video placement", bounds)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no decoded frame: %#x %+v", pixel, v.player.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	trm.mu.Lock()
	trm.termCore.Twrite([]byte("\x1b[?1049l"), false)
	if trm.video != nil {
		t.Error("alternate-screen exit retained video")
	}
	trm.mu.Unlock()
	select {
	case <-v.player.Done():
	case <-time.After(time.Second):
		t.Fatal("decoder survived application exit")
	}
	host.surface.mu.Lock()
	closed := host.surface.closed
	host.surface.mu.Unlock()
	if !closed {
		t.Fatal("video surface survived application exit")
	}
}
