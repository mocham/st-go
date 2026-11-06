package term

import (
	"strings"
	"testing"

	"st-go/config"
	"st-go/limits"
)

type limitHooks struct {
	mockHooks
	titles []string
}

func (h *limitHooks) SetTitle(title string) { h.titles = append(h.titles, title) }

func TestOversizedControlStringRecovery(t *testing.T) {
	for _, prefix := range []string{"\x1b]2;", "\x1bPclear;"} {
		t.Run(prefix, func(t *testing.T) {
			hooks := &limitHooks{}
			trm := NewTerm(config.Default(), hooks)
			trm.Twrite([]byte("keep"+prefix), false)
			chunk := []byte(strings.Repeat("a", 16384))
			for n := 0; n <= limits.ControlBytes; n += len(chunk) {
				trm.Twrite(chunk, false)
			}
			if !trm.strescseq.discard || len(trm.strescseq.buf) != 0 {
				t.Fatal("oversized string retained instead of discarded")
			}
			trm.Twrite([]byte("\x1b\\"), false)
			if len(hooks.titles) != 0 || !strings.HasPrefix(trm.LineText(0), "keep") {
				t.Fatal("executed a truncated control string")
			}
			trm.Twrite([]byte("ok\x1b]2;normal\a"), false)
			if !strings.HasPrefix(trm.LineText(0), "keepok") || len(hooks.titles) != 1 || hooks.titles[0] != "normal" {
				t.Fatal("parser did not recover after the terminator")
			}
		})
	}
}

func TestOversizedResizeKeepsScreen(t *testing.T) {
	trm, _ := newTestTerm(t, 10, 3)
	trm.Twrite([]byte("keep"), false)
	trm.Tresize(1<<30, 1<<30)
	trm.Redraw()
	if trm.Cols() != 10 || trm.Rows() != 3 || !strings.HasPrefix(trm.LineText(0), "keep") {
		t.Fatal("rejected resize changed the screen")
	}
}

func TestZeroTabspaces(t *testing.T) {
	cfg := config.Default()
	cfg.Tabspaces = 0
	trm := NewTerm(cfg, &mockHooks{})
	trm.Twrite([]byte("\tX"), false)
	if trm.LineAt(8, 0).U != 'X' {
		t.Fatal("default tab spacing was not used")
	}
}
