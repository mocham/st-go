//go:build linux

package audio

import "context"

// PCM is a single-worker, direct ALSA stereo S16LE stream. Rate reports the
// negotiated hardware rate so native media decoders can resample correctly.
type PCM struct{ player *alsaPlayer }

func OpenPCM(rate int) (*PCM, error) {
	p, err := openALSA(rate)
	if err != nil {
		return nil, err
	}
	return &PCM{player: p}, nil
}
func (p *PCM) Rate() int { return int(p.player.rate) }
func (p *PCM) Write(ctx context.Context, samples []byte) error {
	return p.player.sendContext(ctx, samples)
}
func (p *PCM) Close(drain bool) { p.player.closeDevice(drain) }
