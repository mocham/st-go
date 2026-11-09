package main

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"st-go/config"
	"st-go/term"
)

// Replay real ANSI output through the backend-neutral terminal core, not a
// substring approximation (unchanged spaces need not be resent).
type paintTerminalHooks struct{ term.Hooks }

func (paintTerminalHooks) ImageClearAll()        {}
func (paintTerminalHooks) SetMode(bool, uint)    {}
func (paintTerminalHooks) SetCursor(int) bool    { return false }
func (paintTerminalHooks) StartDraw() bool       { return false }
func (paintTerminalHooks) SetPointerMotion(bool) {}
func (paintTerminalHooks) Bell()                 {}
func newPaintTerminal(cols, rows int) *term.Term {
	cfg := config.Default()
	cfg.Cols, cfg.Rows = uint(cols), uint(rows)
	return term.NewTerm(cfg, paintTerminalHooks{})
}

func TestCellDiffHandlesWideAndCombiningCharacters(t *testing.T) {
	frames := [][][]tile{
		{{{text: "a"}, {text: "界"}, {continuation: true}, {text: "e\u0301"}, {text: "x"}, {text: " "}}},
		{{{text: "a"}, {text: "b"}, {text: "c"}, {text: "e\u0301"}, {text: "x"}, {text: " "}}},
		{{{text: "a"}, {text: "b"}, {text: "界"}, {continuation: true}, {text: "x"}, {text: " "}}},
		{{{text: "a"}, {text: "b"}, {text: "界", style: searchStyle}, {continuation: true, style: searchStyle}, {text: "x"}, {text: " "}}},
	}
	var r renderer
	screen := newPaintTerminal(6, 2)
	screen.Twrite([]byte("\x1b[?7l"), false)
	for index, frame := range frames {
		var out strings.Builder
		if err := r.paint(frame, 0, 0, 2, &out); err != nil {
			t.Fatal(err)
		}
		screen.Twrite([]byte(out.String()), false)
		for x, cell := range frame[0] {
			glyph := screen.LineAt(x, 0)
			if cell.continuation {
				if glyph.Mode&term.ATTRWdummy == 0 {
					t.Fatalf("frame %d cell %d lost wide continuation", index, x)
				}
				continue
			}
			if glyph.U != []rune(cell.text)[0] {
				t.Fatalf("frame %d cell %d=%q want %q output=%q", index, x, glyph.U, cell.text, out.String())
			}
		}
	}
}

func TestInertModeChangesDoNotRepaintDocument(t *testing.T) {
	e := memoryEditor("UNIQUE_DOCUMENT_TEXT\nsecond unchanged line\n")
	defer e.close(false)
	var renderer renderer
	var out strings.Builder
	if err := e.draw(&renderer, &out); err != nil {
		t.Fatal(err)
	}
	for _, keys := range []string{"i", "<Esc>", "v", "<Esc>", ":", "<Esc>"} {
		out.Reset()
		editKeys(e, keys)
		if err := e.draw(&renderer, &out); err != nil {
			t.Fatal(err)
		}
		output := out.String()
		if strings.Contains(output, "UNIQUE_DOCUMENT_TEXT") || strings.Contains(output, "second unchanged line") || strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\x1b[?25l") {
			t.Fatalf("inert mode %q repainted document: %q", keys, output)
		}
	}
}

func TestEditsWaitForColorsAndPaintDocumentOnlyOnce(t *testing.T) {
	for _, keys := range []string{"x", "dw", "d$", "d?"} {
		t.Run(keys, func(t *testing.T) {
			e := memoryEditor("var number = 42 // edit this row\n// UNIQUE_UNCHANGED_COMMENT\nvar untouched = \"UNIQUE_UNCHANGED_STRING\"\n")
			defer e.close(false)
			b := e.buffer()
			b.meta.Path = "example.go"
			release := make(chan struct{})
			calculate := newSyntaxCalculator()
			e.syntax = newSyntaxHighlighter(func(request syntaxRequest, stop <-chan struct{}) syntaxResult {
				if request.serial > 0 {
					select {
					case <-release:
					case <-stop:
					}
				}
				return calculate(request, stop)
			})
			awaitSyntax(t, e, b)
			var r renderer
			var out strings.Builder
			if err := e.draw(&r, &out); err != nil {
				t.Fatal(err)
			}
			old := append([]tile(nil), r.previous[0]...)
			editKeys(e, keys)
			out.Reset()
			if err := e.draw(&r, &out); err != nil {
				t.Fatal(err)
			}
			// No uncolored OR stale-colored edited document is committed while
			// the worker is blocked. The on-screen row is the previous version.
			for x, cell := range old {
				if r.previous[0][x] != cell {
					t.Fatalf("pending syntax changed document cell %d", x)
				}
			}
			if got := out.String(); strings.Contains(got, "var") || strings.Contains(got, "number") || strings.Contains(got, "UNIQUE_UNCHANGED") || strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[?25l") {
				t.Fatalf("intermediate document repaint: %q", got)
			}
			if keys == "d?" {
				editKeys(e, "<Esc>")
				return
			}
			close(release)
			awaitSyntax(t, e, b)
			out.Reset()
			if err := e.draw(&r, &out); err != nil {
				t.Fatal(err)
			}
			if out.Len() == 0 {
				t.Fatal("colored edit was never committed")
			}
			if strings.Contains(out.String(), "UNIQUE_UNCHANGED") || strings.Contains(out.String(), "\x1b[2J") || strings.Contains(out.String(), "\x1b[?25l") {
				t.Fatalf("colored edit repainted unchanged region: %q", out.String())
			}
			out.Reset()
			if err := e.draw(&r, &out); err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 {
				t.Fatalf("second document pass: %q", out.String())
			}
		})
	}
}

func TestInitialSyntaxFrameWaitsForColors(t *testing.T) {
	e := memoryEditor("var number = 42\n")
	defer e.close(false)
	e.buffer().meta.Path = "example.go"
	release := make(chan struct{})
	calculate := newSyntaxCalculator()
	e.syntax = newSyntaxHighlighter(func(request syntaxRequest, stop <-chan struct{}) syntaxResult {
		select {
		case <-release:
		case <-stop:
		}
		return calculate(request, stop)
	})
	var r renderer
	var out strings.Builder
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "var") || strings.Contains(out.String(), "number") {
		t.Fatalf("initial uncolored document pass: %q", out.String())
	}
	close(release)
	awaitSyntax(t, e, e.buffer())
	out.Reset()
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "var") {
		t.Fatal("initial colored document was not painted")
	}
}

func TestSyntaxSplitDemandSettles(t *testing.T) {
	e := memoryEditor(strings.Repeat("var n = 42\n", 500))
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "example.go"
	e.execute("split")
	e.setPos(b.start(400))
	var r renderer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.pollSyntax()
		if err := e.draw(&r, io.Discard); err != nil {
			t.Fatal(err)
		}
		if b.syntax.ready && !b.syntax.waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !b.syntax.ready || b.syntax.waiting {
		t.Fatal("split demands did not settle")
	}
	for i := 0; i < 4; i++ {
		if err := e.draw(&r, io.Discard); err != nil {
			t.Fatal(err)
		}
		if b.syntax.waiting {
			t.Fatal("split views alternated syntax requests")
		}
	}
}

func TestColorChangePaintsOnlyChangedCell(t *testing.T) {
	frame := func(style string) [][]tile {
		return [][]tile{{{text: "a", style: baseStyle}, {text: "b", style: style}, {text: "c", style: baseStyle}}}
	}
	var r renderer
	var out strings.Builder
	r.paint(frame(baseStyle), 0, 0, 2, &out)
	out.Reset()
	r.paint(frame(searchStyle), 0, 0, 2, &out)
	if strings.Contains(out.String(), "abc") || strings.Contains(out.String(), "c") || !strings.Contains(out.String(), "\x1b[1;2H"+searchStyle+"b") {
		t.Fatalf("color-only change repainted neighbors: %q", out.String())
	}
	out.Reset()
	r.paint(frame(searchStyle), 0, 0, 2, &out)
	if out.Len() != 0 {
		t.Fatalf("identical frame produced output: %q", out.String())
	}
}

// Decode the actual renderer output, recording every row receiving text or an
// erase command, even if it rewrites identical cells or just blank padding.
// Replaying through the terminal core keeps cursor/Unicode handling real.
func emittedPaintRows(t *testing.T, screen *term.Term, output string) map[int]bool {
	t.Helper()
	written := map[int]bool{}
	screen.SetPaintFn(func() {})
	for len(output) > 0 {
		n := 0
		if strings.HasPrefix(output, "\x1b[") {
			n = 2
			for n < len(output) && (output[n] < 0x40 || output[n] > 0x7e) {
				n++
			}
			if n == len(output) {
				t.Fatalf("unterminated renderer CSI: %q", output)
			}
			switch output[n] {
			case 'H', 'm', 'q', 'h', 'l':
			case 'J':
				for y := 0; y < screen.Rows(); y++ {
					written[y] = true
				}
			default:
				t.Fatalf("unexpected renderer CSI: %q", output[:n+1])
			}
			n++
		} else {
			r, size := utf8.DecodeRuneInString(output)
			if r < 32 || r == 127 {
				t.Fatalf("unexpected renderer control: %q", output)
			}
			n = size
			written[screen.CursorY()] = true
		}
		if got := screen.Twrite([]byte(output[:n]), false); got != n {
			t.Fatalf("renderer output consumed %d of %d bytes", got, n)
		}
		output = output[n:]
	}
	screen.TakeRegions()
	return written
}

func TestLocalEditsEmitOnlyChangedRows(t *testing.T) {
	for _, test := range []struct{ path, line string }{
		{"example.go", "var number = 42 // unchanged tail\n"},
		{"example.tex", "\\section{Title} $x^2$ % unchanged tail\n"},
		{"example.md", "A **strong** word and `code`.\n"},
		{"example.sh", "printf '%s\\n' \"$HOME\" # unchanged tail\n"},
		{"example.txt", "Plain text with unchanged trailing rows.\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			e := memoryEditor(strings.Repeat(test.line, 30))
			defer e.close(false)
			b := e.buffer()
			b.meta.Path = test.path
			e.setPos(b.start(2) + 4)
			awaitSyntax(t, e, b)
			var r renderer
			screen := newPaintTerminal(e.cols, e.rows)
			screen.Twrite([]byte("\x1b[?7l"), false)
			draw := func(t *testing.T) int {
				t.Helper()
				before := r.previous
				var out strings.Builder
				if err := e.draw(&r, &out); err != nil {
					t.Fatal(err)
				}
				rows := emittedPaintRows(t, screen, out.String())
				for y := range rows {
					if y < len(before) && slices.Equal(before[y], r.previous[y]) {
						t.Fatalf("unchanged row %d received output: %q", y+1, out.String())
					}
					if len(before) > 0 && y < e.rows-2 && y != 2 {
						t.Fatalf("local edit repainted document row %d: %q", y+1, out.String())
					}
				}
				return out.Len()
			}
			draw(t)
			for _, keys := range []string{"i", "X", "Y", "<BS>", "<Esc>", "x", "u", "<C-R>"} {
				t.Run("keys_"+keys, func(t *testing.T) {
					editKeys(e, keys)
					// Check both the pending-syntax frame and its colored commit.
					draw(t)
					awaitSyntax(t, e, b)
					draw(t)
					if bytes := draw(t); bytes != 0 {
						t.Fatal("settled redraw emitted output")
					}
				})
			}
			// Force a fresh full syntax calculation with identical final colors.
			// A worker result alone must not cause any output, including spaces.
			b.syntax = syntaxCache{}
			awaitSyntax(t, e, b)
			if bytes := draw(t); bytes != 0 {
				t.Fatal("identical recomputed colors emitted output")
			}
		})
	}
}
