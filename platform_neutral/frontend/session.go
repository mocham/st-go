package frontend

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	term "st-go/platform_neutral/stterminal"
	"st-go/ptyutil"
)

type SessionOptions struct {
	Command   []string
	Env       []string
	Directory string
}

type ptySession struct {
	master *os.File
	child  *exec.Cmd
	once   sync.Once
}

func (s *ptySession) close() {
	s.once.Do(func() {
		s.master.Close()
		if s.child.Process != nil {
			_ = s.child.Process.Kill()
		}
	})
}

// StartSession starts one independently managed PTY. A child exiting only
// closes its own widget; the window host owns last-terminal shutdown policy.
func (t *Terminal) StartSession(options SessionOptions) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed() || t.session != nil || len(options.Command) == 0 {
		return fmt.Errorf("terminal closed, session already started, or empty command")
	}
	if t.termCore == nil {
		t.attachTerm(term.NewTerm(t.cfg, t))
		t.termCore.Tresize(t.cols, t.rows)
	}
	t.loadInputConfig(t.cfg)
	t.setupKeys()
	master, slave, err := ptyutil.Open()
	if err != nil {
		return err
	}
	defer slave.Close()
	rows, cols := t.actualRowsCols()
	if err := ptyutil.SetWinSizePixels(master, rows, cols, cols*t.cw, rows*t.ch); err != nil {
		log.Printf("pty: set size: %v", err)
	}
	env := options.Env
	if env == nil {
		env = backendChildEnv(stChildEnv(t.cfg), t.host)
	}
	child, err := ptyutil.StartDir(slave, options.Command, env, options.Directory)
	if err != nil {
		master.Close()
		return err
	}
	t.session = &ptySession{master: master, child: child}
	t.termCore.SetWriter(func(data []byte) { _, _ = master.Write(data) })
	t.ttyResize = func(rows, cols int) { _ = ptyutil.SetWinSizePixels(master, rows, cols, cols*t.cw, rows*t.ch) }
	t.termCore.Redraw()
	go func() { _ = child.Wait(); t.closeSession() }()
	go t.readSession(t.session, t.termCore)
	return nil
}

// Keep draining background PTYs and sending protocol replies. Visibility only
// controls event routing/presentation, never terminal parsing or child progress.
func (t *Terminal) readSession(session *ptySession, core *term.Term) {
	defer t.closeSession()
	buf := make([]byte, 8192)
	var pending []byte
	master := session.master
	fds := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
	minLatency := durationMilliseconds(t.cfg.MinLatency)
	maxLatency := max(minLatency, durationMilliseconds(t.cfg.MaxLatency))
	for {
		n, err := master.Read(buf)
		if err != nil || n == 0 {
			return
		}
		pending = append(pending, buf[:n]...)
		written := t.writeTerminalOutput(core, pending)
		pending = pending[written:]
		firstOutput := time.Now()
		lastOutput := firstOutput
		for {
			t.mu.Lock()
			paused, hasDamage := core.IsPaintPaused(), core.HasPendingPaint()
			t.mu.Unlock()
			if !hasDamage {
				break
			}
			timeout := 30 * time.Millisecond
			if !paused {
				deadline := minTime(lastOutput.Add(minLatency), firstOutput.Add(maxLatency))
				if !time.Now().Before(deadline) {
					t.paintTerminalOutput(core)
					break
				}
				timeout = time.Until(deadline)
			}
			np, err := unix.Poll(fds, pollMilliseconds(timeout))
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				break
			}
			if np == 0 {
				if !paused {
					t.paintTerminalOutput(core)
				}
				break
			}
			// HUP can accompany unread output. Let Read drain it before closing.
			m, err := master.Read(buf)
			if err != nil || m == 0 {
				return
			}
			pending = append(pending, buf[:m]...)
			written := t.writeTerminalOutput(core, pending)
			pending = pending[written:]
			lastOutput = time.Now()
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
