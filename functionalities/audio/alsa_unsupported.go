//go:build !linux

package audio

import (
	"context"
	"fmt"
)

const maxPCMBufferFrames = 65536

type alsaPlayer struct {
	rate, bufferFrames uint32
}

func openALSA(int) (*alsaPlayer, error)     { return nil, fmt.Errorf("ALSA audio requires Linux") }
func (*alsaPlayer) send([]byte) error       { return fmt.Errorf("ALSA audio requires Linux") }
func (*alsaPlayer) close()                  {}
func playMP3(context.Context, string) error { return fmt.Errorf("ALSA audio requires Linux") }
func SelectedOutput() (int, bool)           { return 0, false }
func SetOutput(int) bool                    { return false }
func SetVolume(int) bool                    { return false }
