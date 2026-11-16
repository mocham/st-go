//go:build linux && ffmpeg_static && cgo && !purego

package playback

import (
	"os/exec"
	"path/filepath"
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
	// The private decoder must work without an external player or host process.
	t.Setenv("PATH", t.TempDir())
	return path
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
