//go:build linux

package fb

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"st-go/limits"
)

const (
	recordingPath    = "/tmp/record.mp4"
	recordingFPS     = 30
	recordingTimeout = 5 * time.Second
)

// Driver.mu protects the session pointer and stopping flag. The worker owns
// its reusable RGBA frame and encoder; encoding never holds a drawing lock.
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
	output, err := os.CreateTemp(filepath.Dir(path), ".record-*.mp4")
	if err != nil {
		return err
	}
	temporary := output.Name()
	defer os.Remove(temporary)
	if err := output.Close(); err != nil {
		return err
	}
	if err := encodeRecordingFile(temporary, width, height, stop, capture); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("save recording: %w", err)
	}
	return nil
}
