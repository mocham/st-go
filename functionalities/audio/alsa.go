package audio

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
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"

	"github.com/hajimehoshi/go-mp3"
)

func init() { C.alsa_init_log() }

type alsaPlayer struct{ handle unsafe.Pointer }

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
	if p != nil && p.handle != nil && len(data) != 0 && C.alsa_player_send(p.handle, unsafe.Pointer(&data[0]), C.size_t(len(data))) < 0 {
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

// Preserve the original go-mp3 -> 16-bit stereo -> direct-hardware PCM chain.
// No subprocess, global mixer write, device probing for video, or attenuation.
func playMP3(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("audio requires a regular media file")
	}
	decoder, err := mp3.NewDecoder(f)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	player, err := openALSA(decoder.SampleRate())
	if err != nil {
		return err
	}
	defer player.close()
	buffer := make([]byte, 8192)
	for ctx.Err() == nil {
		n, err := decoder.Read(buffer)
		if n > 0 {
			if err := player.send(buffer[:n]); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func alsaEmitError(message string) {
	c := C.CString(message)
	defer C.free(unsafe.Pointer(c))
	C.alsa_emit_error(c)
}
