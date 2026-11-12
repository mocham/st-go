//go:build linux

package fb

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"st-go/limits"
)

const (
	recordingPath    = "/tmp/record.mp4"
	recordingFPS     = 30
	recordingTimeout = 5 * time.Second
)

// Driver.mu protects the session pointer and stopping flag. The worker owns
// its reusable RGBA frame and encoder; pipe writes never hold a drawing lock.
type screenRecording struct {
	stop     chan struct{}
	done     chan struct{}
	stopping bool
}

func (d *Driver) recordingLabelLocked() string {
	if r := d.recording; r != nil {
		if r.stopping {
			return "SAVE"
		}
		return "STOP"
	}
	if d.recordingFailed {
		return "REC!"
	}
	return "REC"
}

func (d *Driver) recordingChangedLocked() {
	if d.systemBar != nil {
		d.systemBar.nextRefresh = time.Time{}
	}
	d.inputWake.signal()
}

func (d *Driver) toggleRecording() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	if r := d.recording; r != nil {
		d.stopRecordingLocked(r)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.startRecording(recordingPath)
}

func (d *Driver) startRecording(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.active || d.recording != nil {
		return
	}
	r := &screenRecording{stop: make(chan struct{}), done: make(chan struct{})}
	d.recording, d.recordingFailed = r, false
	d.recordingChangedLocked()
	w, h := d.screen.layout.width, d.screen.layout.height
	go func() {
		err := encodeScreenRecording(path, w, h, r.stop, d.captureRecordingFrame)
		if err != nil {
			limits.Report("fb recording", "%v", err)
		}
		d.mu.Lock()
		d.recording, d.recordingFailed = nil, err != nil
		d.recordingChangedLocked()
		close(r.done)
		d.mu.Unlock()
	}()
}

func (d *Driver) stopRecordingLocked(r *screenRecording) {
	if !r.stopping {
		r.stopping = true
		close(r.stop)
		d.recordingChangedLocked()
	}
}

// Cleanup joins the encoder before releasing backend resources. Close itself
// remains nonblocking when Run owns cleanup and the terminal lock is held.
func (d *Driver) finishRecording() {
	d.mu.Lock()
	r := d.recording
	if r != nil {
		d.stopRecordingLocked(r)
	}
	d.mu.Unlock()
	if r != nil {
		<-r.done
	}
}

func (d *Driver) captureRecordingFrame(frame *image.RGBA) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Retain the last complete frame during paint transactions, VT switches,
	// and native-player handoff. Never sample another owner's framebuffer.
	if d.closed || !d.active || d.paintDepth != 0 {
		return
	}
	d.copyScreenshotLocked(frame, frame.Bounds())
	if d.pointerVisible {
		clip := pointerBounds(d.pointer).Intersect(frame.Bounds())
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				if pixel, ok := pointerPixel(x-d.pointer.X, y-d.pointer.Y); ok {
					at := y*frame.Stride + x*4
					frame.Pix[at], frame.Pix[at+1], frame.Pix[at+2], frame.Pix[at+3] = byte(pixel>>16), byte(pixel>>8), byte(pixel), 255
				}
			}
		}
	}
}

// Keep the previous recording until a complete MP4 is ready, just as PNG
// screenshots are published by rename. In particular, a missing/failed encoder
// must not truncate an existing recording or leave a partial file in its place.
func encodeScreenRecording(path string, width, height int, stop <-chan struct{}, capture func(*image.RGBA)) error {
	if _, ok := limits.RGBA("fb recording", width, height); !ok {
		return fmt.Errorf("invalid recording dimensions")
	}
	encoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("screen recording requires ffmpeg: %w", err)
	}
	output, err := os.CreateTemp(filepath.Dir(path), ".record-*.mp4")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	if err := output.Close(); err != nil {
		return err
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
		"-crf", "23", "-pix_fmt", "yuv420p", "-fps_mode", "cfr", "-movflags", "+faststart", temporary)
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
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("save recording: %w", err)
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
