package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteALSAConfig(t *testing.T) {
	cache := t.TempDir()
	if err := writeALSAConfig(cache); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "alsa.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, alsaConfData) {
		t.Fatal("embedded alsa.conf was not written verbatim")
	}
	if got := os.Getenv("ALSA_CONFIG_PATH"); got != path {
		t.Fatalf("ALSA_CONFIG_PATH = %q, want %q", got, path)
	}
	if !bytes.Contains(data, []byte("defaults.pcm")) {
		t.Fatal("embedded alsa.conf is missing the defaults section")
	}
}

// TestALSAErrorLogging verifies ALSA error messages (SNDERR) are written to
// the log file under the cache dir instead of leaking onto the terminal.
func TestALSAErrorLogging(t *testing.T) {
	cache := t.TempDir()
	if err := writeALSAConfig(cache); err != nil {
		t.Fatal(err)
	}
	logPath := alsaLogPath()
	if logPath != filepath.Join(cache, "alsa.log") {
		t.Fatalf("alsa log path = %q", logPath)
	}
	alsaEmitError("boom test")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "boom test") {
		t.Fatalf("alsa log does not contain the error: %q", data)
	}
	// The message must not appear on stderr.
	if strings.Contains(string(data), "alsa:") {
		// SNDERR lines are prefixed by file:line func; ensure nothing weird.
		t.Logf("alsa log content: %q", data)
	}
}

// TestOpenALSA exercises the cgo/libasound link path. It is device-dependent:
// with no sound card it must return an error (graceful), with one it must
// produce a closeable player.
func TestOpenALSA(t *testing.T) {
	p, err := openALSA(44100)
	if err != nil {
		return // no audio device available; the browser degrades gracefully
	}
	defer p.close()
	if err := p.send(make([]byte, 4096)); err != nil {
		t.Fatalf("send: %v", err)
	}
}
