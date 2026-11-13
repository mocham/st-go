//go:build !linux

package audio

import (
	"context"
	"fmt"
)

type PCM struct{}

func OpenPCM(int) (*PCM, error)                  { return nil, fmt.Errorf("PCM playback requires Linux") }
func (*PCM) Rate() int                           { return 0 }
func (*PCM) Write(context.Context, []byte) error { return fmt.Errorf("PCM playback requires Linux") }
func (*PCM) Close(bool)                          {}
