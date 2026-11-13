package frontend

import (
	"bytes"
	"testing"
	"time"

	"st-go/config"
	"st-go/platform_dependent/web"
	"st-go/platform_neutral/api"
	"st-go/widgets"
)

type inputMethodTestHost struct {
	api.Host
	backend string
}

type imeBarTestHost struct {
	*web.Driver
	backend string
}

func (h *imeBarTestHost) BackendName() string { return h.backend }

func TestTerminalIMEUsesSingleTabBarWithTabsDisabled(t *testing.T) {
	for _, backend := range []string{"x11", "fb"} {
		t.Run(backend, func(t *testing.T) {
			native, err := web.New(api.WindowOptions{Cols: 80, Rows: 24, CellWidth: 8, CellHeight: 16, Border: 1}, web.Options{Port: 0})
			if err != nil {
				t.Fatal(err)
			}
			widget, err := widgets.Wrap(&imeBarTestHost{Driver: native, backend: backend})
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.GlyphWidth, cfg.GlyphHeight, cfg.GlyphBaseline = 8, 16, 12
			cfg.WebPCachePath = ""
			trm, err := NewTerminalWidget(cfg, widget, 1)
			if err != nil {
				t.Fatal(err)
			}
			configureTerminalTabs(trm, false)
			m, ok := trm.tabs.(*terminalTabs)
			if !ok {
				t.Fatal("missing helper layout")
			}
			if m.bar == nil || !m.bar.Visible() || m.bar.Bounds().Overlaps(widget.Bounds()) {
				t.Fatal("helper did not reserve its own tab band")
			}
			if cfg.TabSpawnToken != "" || m.allowed {
				t.Fatal("helper enabled tab spawning")
			}
			done := make(chan struct{})
			go func() { m.control.RunWindow(); close(done) }()
			defer func() { trm.mu.Lock(); trm.Close(); trm.mu.Unlock(); <-done }()
			window := func(f func()) {
				t.Helper()
				done := make(chan struct{})
				if !m.control.PostWindow(func() { f(); close(done) }) {
					t.Fatal("post failed")
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("helper deadlocked")
				}
			}
			var input bytes.Buffer
			trm.termCore.SetWriter(func(b []byte) { input.Write(b) })
			window(func() {
				if !trm.inputMethod.view.Load().enabled {
					m.click(1, m.imeLeft+1)
				}
			})
			trm.mu.Lock()
			trm.keyPress('[', 0)
			trm.mu.Unlock()
			window(func() {
				if trm.inputMethod.view.Load().text != "[" || m.imeLeft < 0 {
					t.Error("first bracket not published to tab bar")
				}
				m.click(1, m.imeLeft+1)
			})
			trm.mu.Lock()
			if input.String() != "[" || trm.inputMethod.view.Load().enabled {
				t.Error("tab-bar toggle failed to release bracket")
			}
			trm.mu.Unlock()
			window(func() {
				m.resize(40, 80)
				if m.bar.Bounds().Overlaps(widget.Bounds()) || m.imeLeft < 0 || m.imeRight > 40 {
					t.Error("narrow helper layout overlaps or overflows")
				}
			})
		})
	}
}

func (h *inputMethodTestHost) BackendName() string { return h.backend }

func TestTerminalIMEDefaultsToggleAndInputOrdering(t *testing.T) {
	for _, backend := range []string{"x11", "fb"} {
		t.Run(backend, func(t *testing.T) {
			trm := regionTestTerminal(t)
			host := &inputMethodTestHost{backend: backend}
			trm.host = host
			defer func() { trm.host = nil }()
			trm.eventWake = make(chan struct{}, 1)
			trm.loadInputConfig(config.Default())
			var input bytes.Buffer
			trm.termCore.SetWriter(func(data []byte) { input.Write(data) })
			trm.initIME()
			if trm.inputMethod.view.Load().enabled != (backend == "x11") {
				t.Fatal("wrong IME default")
			}
			if trm.inputMethod.view.Load().enabled {
				trm.toggleIME()
			}
			trm.textInput("[[[ni1]]]")
			if input.String() != "[[[ni1]]]" {
				t.Fatal("disabled IME captured input", input.String())
			}
			trm.toggleIME()
			input.Reset()
			trm.keyPress('[', 0)
			trm.keyPress('[', 0)
			if input.Len() != 0 {
				t.Fatal("partial trigger leaked")
			}
			if trm.inputMethod.view.Load().text != "[[" {
				t.Fatal("partial trigger missing from helper")
			}
			trm.keyPress(XKRight, 0)
			if input.String() != "[[\x1b[C" {
				t.Fatalf("native key reordered: %q", input.String())
			}
			input.Reset()
			trm.textInput("[[[ni1]]]")
			trm.keyPress(XKReturn, 0)
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for trm.inputMethod.stream.Active() {
				select {
				case <-trm.eventWake:
					trm.pollIME()
				case <-deadline.C:
					t.Fatal("IME worker stalled")
				}
			}
			if input.String() != "你\r" {
				t.Fatalf("PTY commit/queued Enter: %q", input.String())
			}
			input.Reset()
			trm.textInput("[[[ABC")
			trm.keyPress(XKEscape, 0)
			if input.String() != "ABC" || trm.inputMethod.stream.Active() {
				t.Fatal("Escape did not end composition without reaching PTY")
			}
			input.Reset()
			trm.keyPress('[', 0)
			trm.toggleIME()
			if input.String() != "[" || trm.inputMethod.view.Load().enabled {
				t.Fatal("toggle lost captured bracket")
			}
		})
	}
}
