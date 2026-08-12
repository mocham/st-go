package main

/*
#include <stddef.h>
#include <stdlib.h>

extern void alsa_init_log(void);
extern void alsa_emit_error(const char *msg);
extern void *alsa_player_open(unsigned int sample_rate, int channels);
extern int alsa_player_send(void *handle, const void *data, size_t bytes);
extern void alsa_player_close(void *handle);
*/
import "C"

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

//go:embed alsa.conf
var alsaConfData []byte

// alsaConfPath is where the embedded ALSA configuration is materialized so
// the statically linked libasound can find it (no system alsa.conf exists for
// a static binary). It lives under the browser's cache dir, which is removed
// on exit.
var alsaConfPath string

// writeALSAConfig materializes the embedded alsa.conf, points ALSA_CONFIG_PATH
// at it, and installs the ALSA error logger (SNDERR -> alsa.log under the cache
// dir instead of stderr). It must run before the first libasound call.
func writeALSAConfig(cacheDir string) error {
	path := filepath.Join(cacheDir, "alsa.conf")
	if err := os.WriteFile(path, alsaConfData, 0644); err != nil {
		return err
	}
	alsaConfPath = path
	if err := os.Setenv("ALSA_CONFIG_PATH", path); err != nil {
		return err
	}
	if err := os.Setenv("ST_GO_ALSA_LOG", filepath.Join(cacheDir, "alsa.log")); err != nil {
		return err
	}
	C.alsa_init_log()
	return nil
}

// alsaLogPath returns the path ALSA errors are logged to.
func alsaLogPath() string {
	return os.Getenv("ST_GO_ALSA_LOG")
}

// alsaEmitError routes a message through the ALSA error handler (used by tests
// to verify logging).
func alsaEmitError(msg string) {
	c := C.CString(msg)
	defer C.free(unsafe.Pointer(c))
	C.alsa_emit_error(c)
}

// alsaPlayer wraps a libasound playback stream. A nil *alsaPlayer or a closed
// one sends nothing.
type alsaPlayer struct {
	handle unsafe.Pointer
}

// openALSA opens a 16-bit stereo playback stream at the given sample rate.
func openALSA(sampleRate int) (*alsaPlayer, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("invalid sample rate %d", sampleRate)
	}
	h := C.alsa_player_open(C.uint(sampleRate), 2)
	if h == nil {
		return nil, fmt.Errorf("cannot open the ALSA playback device")
	}
	return &alsaPlayer{handle: h}, nil
}

func (p *alsaPlayer) send(data []byte) error {
	if p == nil || p.handle == nil || len(data) == 0 {
		return nil
	}
	n := C.alsa_player_send(p.handle, unsafe.Pointer(&data[0]), C.size_t(len(data)))
	if n < 0 {
		return fmt.Errorf("alsa write failed")
	}
	return nil
}

func (p *alsaPlayer) close() {
	if p != nil && p.handle != nil {
		C.alsa_player_close(p.handle)
		p.handle = nil
	}
}
