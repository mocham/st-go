package video

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"st-go/audio"
	"st-go/platform_neutral/api"
)

// Player owns one decoder worker and a bounded control queue. Present must
// consume/copy the pixels before returning. It never runs under Player.mu.
type Player struct {
	mu            sync.Mutex
	status        api.VideoStatus
	width, height int
	commands      chan api.VideoRequest
	wake          chan struct{}
	cancel        context.CancelFunc
	done          chan struct{}
	present       func([]uint32, int, int)
}

func Start(path string, width, height, volume int, muted bool, present func([]uint32, int, int)) *Player {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Player{width: width, height: height, commands: make(chan api.VideoRequest, 16), wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}), present: present,
		status: api.VideoStatus{Supported: Supported(), State: "loading", Volume: volume, Muted: muted}}
	go func() {
		defer close(p.done)
		err := p.run(ctx, path)
		p.mu.Lock()
		defer p.mu.Unlock()
		if ctx.Err() != nil {
			p.status.State = "closed"
		} else if err != nil {
			p.status.State = "error"
			p.status.Error = err.Error()
		}
	}()
	return p
}
func (p *Player) Status() api.VideoStatus { p.mu.Lock(); defer p.mu.Unlock(); return p.status }
func (p *Player) Close()                  { p.cancel() }
func (p *Player) Done() <-chan struct{}   { return p.done }
func (p *Player) Resize(w, h int) {
	p.mu.Lock()
	p.width, p.height = w, h
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *Player) Control(r api.VideoRequest) error {
	select {
	case <-p.done:
		return fmt.Errorf("video is closed")
	default:
	}
	select {
	case p.commands <- r:
		return nil
	default:
		return fmt.Errorf("video control queue is full")
	}
}

func (p *Player) run(ctx context.Context, path string) error {
	if !Supported() {
		return fmt.Errorf("video playback requires the full st-fb build")
	}
	d, err := openDecoder(ctx, path)
	if err != nil {
		return err
	}
	defer d.close()
	info := d.info()
	p.mu.Lock()
	p.status.State, p.status.Width, p.status.Height, p.status.Audio, p.status.DurationMS = "playing", info.width, info.height, info.audio, info.duration.Milliseconds()
	volume, muted := p.status.Volume, p.status.Muted
	p.mu.Unlock()
	var pcm *audio.PCM
	audioTried := false
	dropAudio := func() {
		if pcm != nil {
			pcm.Close(false)
			pcm = nil
		}
		audioTried = false
	}
	defer dropAudio()
	warn := func(err error) { p.mu.Lock(); p.status.Warning = err.Error(); p.mu.Unlock() }
	render := func() error {
		p.mu.Lock()
		w, h := p.width, p.height
		p.mu.Unlock()
		if w < 1 || h < 1 {
			return nil
		}
		pixels, err := d.pixels(w, h)
		if err == nil && p.present != nil {
			p.present(pixels, w, h)
		}
		return err
	}
	paused, ended, pending, preview := false, false, false, false
	var kind int
	var pts, position, skipTo time.Duration
	var lastPresentation time.Time
	base := time.Now()
	seek := func(target time.Duration) error {
		if info.duration > 0 {
			target = min(target, max(0, info.duration-time.Millisecond))
		}
		target = max(0, target)
		if err := d.seek(target); err != nil {
			return err
		}
		dropAudio()
		position, skipTo = target, target
		base = time.Now().Add(-target)
		pending, ended, preview = false, false, true
		return nil
	}
	handle := func(r api.VideoRequest) error {
		switch r.Action {
		case "toggle":
			if ended {
				if err := seek(0); err != nil {
					return err
				}
				paused = false
			} else {
				paused = !paused
			}
			base = time.Now().Add(-position)
			dropAudio()
		case "seek":
			if err := seek(time.Duration(r.PositionMS) * time.Millisecond); err != nil {
				return err
			}
		case "volume":
			volume = r.Volume
		case "mute":
			muted = r.Muted
			dropAudio()
		}
		return nil
	}
	publish := func() {
		state := "playing"
		if paused {
			state = "paused"
		}
		if ended {
			state = "ended"
		}
		p.mu.Lock()
		p.status.State, p.status.PositionMS, p.status.Volume, p.status.Muted = state, position.Milliseconds(), volume, muted
		p.mu.Unlock()
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		publish()
		// Always service control input before decoding another packet.
		select {
		case r := <-p.commands:
			if err := handle(r); err != nil {
				return err
			}
			continue
		case <-p.wake:
			if err := render(); err != nil {
				return err
			}
		default:
		}
		if (paused && !preview) || ended {
			select {
			case <-ctx.Done():
				return nil
			case r := <-p.commands:
				if err := handle(r); err != nil {
					return err
				}
			case <-p.wake:
				if err := render(); err != nil {
					return err
				}
			}
			continue
		}
		if !pending {
			kind, pts, err = d.next()
			if err != nil {
				return err
			}
			if kind == 0 {
				ended = true
				if pcm != nil {
					pcm.Close(true)
					pcm = nil
				}
				continue
			}
			if pts < skipTo {
				continue
			}
			if preview && kind != 1 {
				continue
			}
			pts = max(0, pts)
			pending = true
		}
		if !preview {
			wait := time.Until(base.Add(pts))
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case r := <-p.commands:
					timer.Stop()
					if err := handle(r); err != nil {
						return err
					}
					continue
				case <-p.wake:
					timer.Stop()
					if err := render(); err != nil {
						return err
					}
					continue
				case <-timer.C:
				}
			}
		}
		pending = false
		if kind == 1 {
			position = pts
			// Decode reference pictures even when late. Still display occasional
			// frames if the machine cannot decode at the source frame rate.
			if preview || time.Since(base.Add(pts)) < 150*time.Millisecond || time.Since(lastPresentation) >= 250*time.Millisecond {
				if err := render(); err != nil {
					return err
				}
				lastPresentation = time.Now()
			}
			if preview {
				base = time.Now().Add(-pts)
				preview = false
			}
		} else if !muted {
			if !audioTried {
				audioTried = true
				pcm, err = audio.OpenPCM(48000)
				if err != nil {
					warn(err)
				}
			}
			if pcm != nil {
				samples, err := d.audio(pcm.Rate())
				if err != nil {
					return err
				}
				scalePCM(samples, volume)
				writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				err = pcm.Write(writeCtx, samples)
				cancel()
				if err != nil {
					warn(err)
					pcm.Close(false)
					pcm = nil
				}
			}
		}
	}
}

func scalePCM(samples []byte, volume int) {
	if volume == 100 {
		return
	}
	for i := 0; i+1 < len(samples); i += 2 {
		n := int16(binary.LittleEndian.Uint16(samples[i:]))
		binary.LittleEndian.PutUint16(samples[i:], uint16(int16(int(n)*volume/100)))
	}
}
