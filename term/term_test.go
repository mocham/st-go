package term

import (
	"strings"
	"testing"

	"st-go/config"
)

type mockHooks struct {
	screen       []string
	imageOptions ImageDecodeOptions
	imageCols    int
	imageRows    int
	imageGlyphs  []Glyph
	imageOK      bool
	geometry     []WindowGeometryRequest
}

func (m *mockHooks) Bell()                                            {}
func (m *mockHooks) ClipCopy()                                        {}
func (m *mockHooks) DrawCursor(a, b int, g Glyph, c, d int, og Glyph) {}
func (m *mockHooks) DrawLine(line []Glyph, x1, y1, x2 int) {
	var sb strings.Builder
	for i, g := range line {
		if i == 0 || g.U != 0 {
			if g.U != 0 {
				sb.WriteRune(g.U)
			}
		}
	}
	if len(m.screen) <= y1 {
		m.screen = append(m.screen, make([]string, y1+1-len(m.screen))...)
	}
	m.screen[y1] = sb.String()
}
func (m *mockHooks) FinishDraw()                             {}
func (m *mockHooks) LoadCols()                               {}
func (m *mockHooks) SetColorName(i int, s string) bool       { return false }
func (m *mockHooks) GetColor(i int) (byte, byte, byte, bool) { return 0, 0, 0, false }
func (m *mockHooks) SetIconTitle(s string)                   {}
func (m *mockHooks) SetTitle(s string)                       {}
func (m *mockHooks) SetCursor(shape int) bool                { return true }
func (m *mockHooks) SetMode(set bool, mode uint)             {}
func (m *mockHooks) SetPointerMotion(on bool)                {}
func (m *mockHooks) SetSel(s string)                         {}
func (m *mockHooks) StartDraw() bool                         { return true }
func (m *mockHooks) ImageDecode(encoded []byte, opts ImageDecodeOptions) (int, int, []Glyph, bool) {
	m.imageOptions = opts
	return m.imageCols, m.imageRows, m.imageGlyphs, m.imageOK
}
func (m *mockHooks) ImageClearAll()                           {}
func (m *mockHooks) WindowGeometry(req WindowGeometryRequest) { m.geometry = append(m.geometry, req) }

func newTestTerm(t *testing.T, cols, rows int) (*Term, *mockHooks) {
	cfg := config.Default()
	cfg.Cols = uint(cols)
	cfg.Rows = uint(rows)
	m := &mockHooks{}
	term := NewTerm(cfg, m)
	return term, m
}

func TestBasicOutput(t *testing.T) {
	term, m := newTestTerm(t, 10, 3)
	term.Twrite([]byte("hello"), false)
	term.Redraw()
	if !strings.Contains(m.screen[0], "hello") {
		t.Fatalf("expected 'hello' on line 0, got %q", m.screen[0])
	}
}

func TestCursorMove(t *testing.T) {
	term, m := newTestTerm(t, 10, 3)
	term.Twrite([]byte("A\x1b[2GB"), false)
	term.Redraw()
	if !strings.HasPrefix(m.screen[0], "A") {
		t.Fatalf("expected A at start, got %q", m.screen[0])
	}
}

func TestNewlineScroll(t *testing.T) {
	term, m := newTestTerm(t, 5, 2)
	for i := 0; i < 5; i++ {
		term.Twrite([]byte(string(rune('a'+i))+"\n"), false)
	}
	term.Redraw()
	if !strings.Contains(m.screen[0], "e") {
		t.Fatalf("expected e visible on top row after scroll, got %q", m.screen[0])
	}
	if strings.Contains(m.screen[0], "a") {
		t.Fatalf("expected a scrolled off, got %q", m.screen[0])
	}
	if strings.Contains(m.screen[1], "a") || strings.Contains(m.screen[1], "e") {
		t.Fatalf("unexpected content on bottom row, got %q", m.screen[1])
	}
}

func TestWideChars(t *testing.T) {
	term, m := newTestTerm(t, 6, 2)
	term.Twrite([]byte("中"), false)
	term.Redraw()
	if !strings.Contains(m.screen[0], "中") {
		t.Fatalf("expected CJK char on screen, got %q", m.screen[0])
	}
}

func TestColors(t *testing.T) {
	term, _ := newTestTerm(t, 10, 2)
	// SGR 38;5;196 sets fg to palette 196
	term.Twrite([]byte("\x1b[38;5;196mX"), false)
	if term.c.attr.Fg != 196 {
		t.Fatalf("expected fg=196, got %d", term.c.attr.Fg)
	}
	// truecolor
	term.Twrite([]byte("\x1b[38;2;1;2;3mY"), false)
	if term.c.attr.Fg != TrueColor(1, 2, 3) {
		t.Fatalf("expected truecolor fg, got %d", term.c.attr.Fg)
	}
}
