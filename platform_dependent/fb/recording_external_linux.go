//go:build linux && (!ffmpeg_static || !cgo || purego)

package fb

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The minimal profile retains the external encoder and needs no native assets.
func encodeRecordingFile(path string, width, height int, stop <-chan struct{}, capture func(*image.RGBA)) error {
	encoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("screen recording requires ffmpeg: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// EOF normally finalizes the MP4. A wedged encoder must still release a
	// blocked pipe writer and allow console cleanup within a bounded time.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-stop:
		}
		timer := time.NewTimer(recordingTimeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			cancel()
		}
	}()
	cmd := exec.CommandContext(ctx, encoder, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "rawvideo", "-pixel_format", "rgba", "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", strconv.Itoa(recordingFPS), "-use_wallclock_as_timestamps", "1", "-i", "pipe:0",
		"-an", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-c:v", "libx264", "-preset", "ultrafast",
		"-crf", "23", "-pix_fmt", "yuv420p", "-fps_mode", "cfr", "-movflags", "+faststart", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	var diagnostic recordingDiagnostic
	cmd.Stderr = &diagnostic
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	ticker := time.NewTicker(time.Second / recordingFPS)
	defer ticker.Stop()
	// Always send at least one frame, including a rapid start/stop click.
frames:
	for {
		capture(frame)
		if pipe, ok := input.(*os.File); ok {
			_ = pipe.SetWriteDeadline(time.Now().Add(recordingTimeout))
		}
		if _, err = input.Write(frame.Pix); err != nil {
			cancel()
			break
		}
		select {
		case <-stop:
			break frames
		case <-ticker.C:
		}
	}
	_ = input.Close()
	if waitErr := cmd.Wait(); waitErr != nil {
		return fmt.Errorf("ffmpeg recording: %w: %s", waitErr, strings.TrimSpace(string(diagnostic)))
	}
	if err != nil {
		return fmt.Errorf("write recording frame: %w", err)
	}
	return nil
}

// ffmpeg's stderr is drained throughout capture, but only a bounded diagnostic
// is retained. Cmd.Wait joins the writer before its contents are read.
type recordingDiagnostic []byte

func (b *recordingDiagnostic) Write(p []byte) (int, error) {
	n := len(p)
	*b = append(*b, p[:min(n, 4096-len(*b))]...)
	return n, nil
}
