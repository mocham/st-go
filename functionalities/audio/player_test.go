package audio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPlaybackCancellationOwnershipAndClose(t *testing.T) {
	started := make(chan struct{})
	completed := make(chan error, 1)
	p := &Player{run: func(ctx context.Context, file string) error {
		close(started)
		<-ctx.Done()
		return nil
	}}
	t.Cleanup(p.Close)
	if err := p.Start("owner", "file.mp3", func(err error) { completed <- err }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := p.Start("second", "file.mp3", func(error) {}); err == nil {
		t.Fatal("accepted an unbounded second playback job")
	}
	p.Stop("other")
	select {
	case <-completed:
		t.Fatal("unrelated job ID stopped playback")
	case <-time.After(10 * time.Millisecond):
	}
	p.Stop("owner")
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not release the playback job")
	}
	p.Close()
	if err := p.Start("new", "file.mp3", func(error) {}); err == nil {
		t.Fatal("started playback after session closed")
	}
}

func TestCloseDoesNotWaitForProtocolCallback(t *testing.T) {
	allowCallback := make(chan struct{})
	returned := make(chan struct{})
	p := &Player{run: func(ctx context.Context, _ string) error { <-ctx.Done(); return nil }}
	if err := p.Start("job", "file", func(error) { <-allowCallback; close(returned) }); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		close(allowCallback)
		t.Fatal("close deadlocked with the caller's protocol lock")
	}
	close(allowCallback)
	<-returned
}

func TestPlaybackPanicIsReportedAndDeviceJobReleased(t *testing.T) {
	p := &Player{run: func(context.Context, string) error { panic("decoder failure") }}
	t.Cleanup(p.Close)
	done := make(chan error, 1)
	if err := p.Start("job", "file", func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "decoder failure") {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("decoder panic escaped completion")
	}
}

func TestALSAErrorLogging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alsa.log")
	t.Setenv("ST_GO_ALSA_LOG", path)
	alsaEmitError("boom test")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "boom test") {
		t.Fatalf("ALSA logging: %s %v", data, err)
	}
}

func TestOpenALSA(t *testing.T) {
	for _, rate := range []int{-1, 0} {
		if player, err := openALSA(rate); err == nil || player != nil {
			t.Fatalf("openALSA(%d) accepted an invalid sample rate", rate)
		}
	}
	p, err := openALSA(44100)
	if err != nil {
		return
	} // No hardware in headless tests.
	defer p.close()
	if p.rate == 0 || p.bufferFrames == 0 || p.bufferFrames > maxPCMBufferFrames {
		t.Fatalf("invalid negotiated PCM configuration: rate=%d buffer=%d", p.rate, p.bufferFrames)
	}
	if err := p.send(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybackRefusesNonRegularInputsWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.mp3")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, fifo} {
		done := make(chan error, 1)
		go func() { done <- playMP3(context.Background(), path) }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("accepted non-regular media", path)
			}
		case <-time.After(time.Second):
			t.Fatal("opening non-regular audio blocked", path)
		}
	}
}
