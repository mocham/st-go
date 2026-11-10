// Package audio owns native MP3 playback for a terminal session. TUI clients
// use terminal protocols instead of importing this native output module.
package audio

import (
	"context"
	"fmt"
	"sync"
)

type job struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

// Player permits one bounded playback job, with no queued media or PCM frames.
// Completion is called after the decoder/device have been closed.
type Player struct {
	mu     sync.Mutex
	active *job
	closed bool
	run    func(context.Context, string) error
}

func (p *Player) Start(id, file string, completed func(error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.active != nil || completed == nil {
		return fmt.Errorf("terminal audio is busy or closed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, cancel: cancel, done: make(chan struct{})}
	p.active = j
	run := p.run
	if run == nil {
		run = playMP3
	}
	go func() {
		err := runSafely(run, ctx, file)
		cancel()
		p.mu.Lock()
		p.active = nil
		close(j.done) // Close may hold the caller's protocol mutex; callback must not precede this.
		p.mu.Unlock()
		completed(err)
	}()
	return nil
}

func runSafely(run func(context.Context, string) error, ctx context.Context, file string) (err error) {
	defer func() {
		if problem := recover(); problem != nil {
			err = fmt.Errorf("audio playback: %v", problem)
		}
	}()
	return run(ctx, file)
}

func (p *Player) Stop(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != nil && p.active.id == id {
		p.active.cancel()
	}
}

func (p *Player) Close() {
	p.mu.Lock()
	p.closed = true
	j := p.active
	if j != nil {
		j.cancel()
	}
	p.mu.Unlock()
	if j != nil {
		<-j.done
	}
}
