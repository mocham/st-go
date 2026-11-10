package audio

/*
#include <stddef.h>
#include <stdlib.h>
extern void alsa_init_log(void);
extern void alsa_emit_error(const char *msg);
extern void *alsa_player_open(unsigned int sample_rate, int channels);
extern int alsa_player_send(void *handle, const void *data, size_t bytes);
extern void alsa_player_close(void *handle);
// Mixer operations use short-lived control handles, never PCM handles.
extern int alsa_selected_output(int card);
extern int alsa_set_output(int card, int mode);
extern int alsa_set_volume(int card, int percent);
*/
import "C"

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
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

// SelectedOutput reads the ALSA output selector without opening a playback
// stream or changing mixer/device state. Mode 0 is the speaker, 1 the earphone
// outlet. Unknown/inaccessible controls never imply that a speaker is selected.
// This mirrors TEST-ASSETS/plugin-snd.c: Headphone and Speaker playback
// switches on the highest of the first two discovered cards. An optional
// numeric ST_GO_AUDIO_OUTPUT_CARD overrides card discovery.
func SelectedOutput() (mode int, known bool) {
	card, ok := outputCard()
	if !ok {
		return 0, false
	}
	mode = int(C.alsa_selected_output(C.int(card)))
	return mode, mode == 0 || mode == 1
}

// SetOutput selects speaker (0) or earphone (1) through the same mixer
// selector used by SelectedOutput. It opens the ALSA control read/write only
// for this click and does not open or reconfigure a PCM stream.
func SetOutput(mode int) bool {
	if mode != 0 && mode != 1 {
		return false
	}
	card, ok := outputCard()
	if !ok {
		return false
	}
	return C.alsa_set_output(C.int(card), C.int(mode)) == 0
}

// SetVolume sets the playback volume of the currently selected Speaker or
// Headphone route. Percentages outside 0..100 are rejected rather than clamped.
func SetVolume(percent int) bool {
	if percent < 0 || percent > 100 {
		return false
	}
	card, ok := outputCard()
	if !ok {
		return false
	}
	return C.alsa_set_volume(C.int(card), C.int(percent)) == 0
}

func outputCard() (card int, ok bool) {
	card = -1
	if value := os.Getenv("ST_GO_AUDIO_OUTPUT_CARD"); value != "" {
		var err error
		card, err = strconv.Atoi(value)
		if err != nil || card < 0 || card > 31 {
			return 0, false
		}
	}
	return card, true
}
