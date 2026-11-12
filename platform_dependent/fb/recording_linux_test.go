//go:build linux

package fb

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func TestRecordingCapturesCompositionAndHoldsCompleteFrameWhileSuspended(t *testing.T) {
	d := memoryDriver(t, 900, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	d.refreshSystemBar(time.Now())
	for i := range d.pixels {
		d.pixels[i] = 0xffaa2030
	}
	d.Upload(d.pixels, 900, 0, 0, 900, 100)
	if _, err := d.NewSurface(image.Rect(10, 10, 50, 50), 0xff1080c0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NewWidgetSurface(api.BarWidget, image.Rect(20, 20, 25, 25), 0xfff0e010); err != nil {
		t.Fatal(err)
	}
	d.SetTextCursor(api.TextCursor{Bounds: image.Rect(15, 15, 16, 16), Pixels: []uint32{0xffffffff}, Base: []uint32{0xff1080c0}})
	d.movePointer(image.Pt(48, 48), true)
	d.Show()
	d.flushPending()
	frame := image.NewRGBA(image.Rect(0, 0, 900, 100))
	d.captureRecordingFrame(frame)
	for y := range 100 {
		for x := range 900 {
			got := frame.RGBAAt(x, y)
			want := binary.NativeEndian.Uint32(d.screen.memory[(y*900+x)*4:]) & 0xffffff
			if uint32(got.R)<<16|uint32(got.G)<<8|uint32(got.B) != want || got.A != 255 {
				t.Fatalf("recording differs from composition at %d,%d: %v vs %#x", x, y, got, want)
			}
		}
	}
	before := bytes.Clone(frame.Pix)
	d.BeginPaint()
	d.Upload([]uint32{0xff00ff00}, 1, 0, 0, 1, 1)
	d.captureRecordingFrame(frame)
	if !bytes.Equal(before, frame.Pix) {
		t.Fatal("recorded an uncommitted paint")
	}
	d.EndPaint()
	d.suspendPresentation()
	d.captureRecordingFrame(frame)
	if !bytes.Equal(before, frame.Pix) {
		t.Fatal("inactive VT changed the recorded frame")
	}
	d.mu.Lock()
	d.active = true
	d.mu.Unlock()
	d.captureRecordingFrame(frame)
	if frame.RGBAAt(0, 0).G != 255 {
		t.Fatal("recording did not resume with the latest content")
	}
}

func requireRecordingEncoder(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
}

func TestRecordingEncodesPlayableMP4AndAtomicallyReplacesPreviousFile(t *testing.T) {
	requireRecordingEncoder(t)
	directory := t.TempDir()
	path, target := filepath.Join(directory, "record.mp4"), filepath.Join(directory, "previous")
	if err := os.WriteFile(target, []byte("previous recording"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	frames := 0
	err := encodeScreenRecording(path, 65, 49, stop, func(frame *image.RGBA) {
		frames++
		for at := 0; at < len(frame.Pix); at += 4 {
			if frames < 6 {
				frame.Pix[at], frame.Pix[at+1] = 220, 20
			} else {
				frame.Pix[at], frame.Pix[at+1] = 20, 220
			}
			frame.Pix[at+2], frame.Pix[at+3] = 20, 255
		}
		if frames == 12 {
			if data, err := os.ReadFile(path); err != nil || string(data) != "previous recording" {
				t.Error("unfinished recording replaced the previous file", string(data), err)
			}
			close(stop)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "previous recording" {
		t.Fatal("recording followed the destination symlink", string(data), err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("recording was not a private regular file", info, err)
	}
	probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name,pix_fmt,width,height,nb_frames", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []struct {
			Codec         string `json:"codec_name"`
			Format        string `json:"pix_fmt"`
			Width, Height int
		}
	}
	if err := json.Unmarshal(probe, &result); err != nil || len(result.Streams) != 1 {
		t.Fatal("invalid video streams", string(probe), err)
	}
	stream := result.Streams[0]
	if stream.Codec != "h264" || stream.Format != "yuv420p" || stream.Width != 66 || stream.Height != 50 {
		t.Fatal("wrong codec or odd-dimension padding", string(probe))
	}
	decoded, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1").Output()
	if err != nil {
		t.Fatal("MP4 could not be decoded", err)
	}
	red, green := false, false
	const frameSize = 66 * 50 * 3
	for at := 0; at+frameSize <= len(decoded); at += frameSize {
		red = red || decoded[at] > 180 && decoded[at+1] < 60
		green = green || decoded[at+1] > 180 && decoded[at] < 60
	}
	if !red || !green {
		t.Fatal("recording did not preserve changing screen contents", red, green, len(decoded)/frameSize)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(directory, ".record-*")); len(leftovers) != 0 {
		t.Fatal("recording left temporary files", leftovers)
	}
}

func waitForRecording(t *testing.T, r *screenRecording) {
	t.Helper()
	if r == nil {
		return
	}
	select {
	case <-r.done:
	case <-time.After(recordingTimeout + 3*time.Second):
		t.Fatal("recording worker did not terminate")
	}
}

func TestRecordingButtonStopsAndShutdownFinalizes(t *testing.T) {
	requireRecordingEncoder(t)
	d := memoryDriver(t, 900, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	path := filepath.Join(t.TempDir(), "record.mp4")
	d.startRecording(path)
	d.refreshSystemBar(time.Now())
	d.mu.Lock()
	r, b := d.recording, d.systemBar
	d.mu.Unlock()
	if r == nil || !strings.Contains(b.text, "STOP") {
		t.Fatal("recording did not activate the stop button")
	}
	d.startRecording(filepath.Join(t.TempDir(), "duplicate.mp4"))
	d.mu.Lock()
	if d.recording != r {
		t.Error("started a second concurrent encoder")
	}
	d.mu.Unlock()
	d.systemBarClick(b.recordingLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	waitForRecording(t, r)
	d.refreshSystemBar(time.Now())
	if !strings.Contains(b.text, "REC") || strings.Contains(b.text, "REC!") {
		t.Fatal("stop did not reset the button", b.text)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("stop did not save recording", err)
	}
	second := filepath.Join(t.TempDir(), "exit.mp4")
	d.startRecording(second)
	d.Close()
	if output, err := exec.Command("ffprobe", "-v", "error", second).CombinedOutput(); err != nil {
		t.Fatal("exit did not finalize a playable MP4", string(output), err)
	}
}

func TestRecordingMissingEncoderReportsErrorAndAllowsKeyboardRetry(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d := memoryDriver(t, 900, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	_, feed := systemBarKeyboard(t, d)
	b := d.systemBar
	d.systemBarClick(b.recordingLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	d.mu.Lock()
	r := d.recording
	d.mu.Unlock()
	waitForRecording(t, r)
	d.refreshSystemBar(time.Now())
	if !strings.Contains(b.text, "REC!") {
		t.Fatal("missing encoder did not produce visible error", b.text)
	}
	// Alt+0, Tab from screenshot to recording, Enter retries the failed start.
	feed(56, 11, 11|128, 56|128, 15, 15|128, 28, 28|128)
	d.mu.Lock()
	r = d.recording
	selected := b.focusedControl
	d.mu.Unlock()
	if selected != systemBarRecording {
		t.Fatal("recording button was not keyboard accessible")
	}
	waitForRecording(t, r)
	d.refreshSystemBar(time.Now())
	if !strings.Contains(b.text, "REC!") {
		t.Fatal("failed retry left button in recording state", b.text)
	}
}

func TestRecordingShutdownKillsStalledEncoderAndPreservesPreviousFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ffmpeg"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	path := filepath.Join(directory, "record.mp4")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	d := memoryDriver(t, 900, 100)
	d.startRecording(path)
	d.mu.Lock()
	r := d.recording
	d.mu.Unlock()
	d.toggleRecording()
	d.toggleRecording() // Repeated stop must not close the channel twice.
	d.mu.Lock()
	label := d.recordingLabelLocked()
	d.mu.Unlock()
	if label != "SAVE" {
		t.Fatal("finalization state was not visible", label)
	}
	waitForRecording(t, r)
	d.Close()
	if data, err := os.ReadFile(path); err != nil || string(data) != "previous" {
		t.Fatal("failed encoder destroyed previous recording", string(data), err)
	}
	d.mu.Lock()
	failed := d.recordingFailed
	d.mu.Unlock()
	if !failed {
		t.Fatal("stalled encoder was reported as a successful save")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(directory, ".record-*")); len(leftovers) != 0 {
		t.Fatal("failed encoder left temporary files", leftovers)
	}
}
