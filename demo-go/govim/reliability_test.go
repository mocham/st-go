package main

import (
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestJournalSyncFailureFreezesUntilPreserved(t *testing.T) {
	e, _ := diskEditor(t, "abc\n", defaultJournalOptions())
	j := e.buffer().journal
	j.options.Bytes = 1
	j.syncFile = func() error { return errors.New("synthetic fsync failure") }
	editKeys(e, "x")
	if editorText(e) != "bc\n" || j.unsynced == 0 {
		t.Fatal("complete journal record was not applied/retained after sync failure")
	}
	editKeys(e, "x")
	if editorText(e) != "bc\n" {
		t.Fatal("editing continued with broken durability")
	}
	if err := e.buffer().save(false); err == nil {
		t.Fatal("save ignored journal sync failure")
	}
	j.syncFile = nil
	e.execute("preserve")
	editKeys(e, "x")
	if editorText(e) != "c\n" {
		t.Fatal("preserve did not unblock edits")
	}
}

func TestRecoveryDoesNotDependOnReadableOriginal(t *testing.T) {
	e, path := diskEditor(t, "abc\n", defaultJournalOptions())
	editKeys(e, "A café<Esc>")
	e.buffer().journal.Close(false)
	if err := os.WriteFile(path, []byte{0xff, 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	r := newEditor(defaultJournalOptions())
	if _, err := r.open(path, true); err != nil {
		t.Fatal(err)
	}
	defer r.close(false)
	if editorText(r) != "abc café\n" {
		t.Fatal("recovery needed intact original")
	}
	if err := r.buffer().save(false); err == nil {
		t.Fatal("recovery overwrote modified original")
	}
}

func TestSaveAsAndReopenBuffer(t *testing.T) {
	e, path := diskEditor(t, "one\n", defaultJournalOptions())
	editKeys(e, "Ax<Esc>")
	dest := filepath.Join(filepath.Dir(path), "other file.txt")
	e.execute("saveas '" + dest + "'")
	if e.buffer().meta.Path != dest || e.buffer().dirty() {
		t.Fatalf("saveas: %s", e.message)
	}
	if data, _ := os.ReadFile(path); string(data) != "one\n" {
		t.Fatal("saveas changed source")
	}
	if data, _ := os.ReadFile(dest); string(data) != "onex\n" {
		t.Fatalf("saveas failed: %q", data)
	}
	if _, err := e.open(path, false); err != nil {
		t.Fatal(err)
	}
	e.open(dest, false)
	if e.buffer().meta.Path != dest {
		t.Fatal("opening existing buffer did not switch")
	}
}

func TestRenameCheckpointRetainsRecoverableHistory(t *testing.T) {
	e, path := diskEditor(t, "one\n", defaultJournalOptions())
	editKeys(e, "Ax<Esc>")
	destination := filepath.Join(filepath.Dir(path), "renamed.txt")
	if err := e.bindPath(destination); err != nil {
		t.Fatal(err)
	}
	e.buffer().journal.Close(false)
	r := newEditor(defaultJournalOptions())
	if _, err := r.open(destination, true); err != nil {
		t.Fatal(err)
	}
	defer r.close(false)
	if !r.buffer().dirty() || editorText(r) != "onex\n" {
		t.Fatal("rename checkpoint lost unsaved state")
	}
	editKeys(r, "u")
	if editorText(r) != "one\n" || !r.buffer().dirty() {
		t.Fatal("rename checkpoint lost undo history")
	}
	editKeys(r, "<C-r>")
	if err := r.buffer().save(false); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicNewFileNeverClobbersRacingCreator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")
	os.WriteFile(path, []byte("other process"), 0600)
	if replaced, err := atomicWrite(path, []byte("ours"), 0600, true); err == nil || replaced {
		t.Fatal("no-replace save overwrote existing file")
	}
	if data, _ := os.ReadFile(path); string(data) != "other process" {
		t.Fatal("racing writer lost data")
	}
}

func TestRandomizedHistoryAndRecovery(t *testing.T) {
	commands := []string{"iα<Esc>", "a界<Esc>", "x", "dd", "p", "P", "u", "<C-r>", "cwX<Esc>", "oabc<Esc>", "Ofoo<Esc>", "j", "k", "w", "b", "gg", "G", "$", "0", ">>", "<<", "J", "~", "viwy", "viwd", "<C-v>jIX<Esc>", "dw", "rQ", "Rxy<BS><Esc>", ".", "yyp"}
	for seed := int64(0); seed < 8; seed++ {
		t.Run(string(rune('a'+seed)), func(t *testing.T) {
			e, path := diskEditor(t, "alpha beta\nγδ 世界\n\nlast\n", defaultJournalOptions())
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 150; i++ {
				editKeys(e, commands[rng.Intn(len(commands))])
				if e.pos() < 0 || e.pos() > len(e.buffer().text) || !utf8.ValidString(editorText(e)) {
					t.Fatal("buffer invariant failed")
				}
				if len(e.buffer().text) > 32768 {
					break
				}
			}
			want := editorText(e)
			b := e.buffer()
			b.journal.Close(false)
			r := newEditor(defaultJournalOptions())
			rb, err := r.open(path, true)
			if err != nil {
				t.Fatal(err)
			}
			defer r.close(false)
			if editorText(r) != want {
				t.Fatalf("recovery diverged: %q != %q", editorText(r), want)
			}
			// Every complete history entry must invert and reapply exactly.
			for i := 0; i < min(20, len(rb.undo)); i++ {
				before := string(rb.text)
				if _, err := rb.undoChange(false); err != nil {
					t.Fatal(err)
				}
				if _, err := rb.undoChange(true); err != nil {
					t.Fatal(err)
				}
				if string(rb.text) != before {
					t.Fatal("undo/redo mismatch")
				}
				if _, err := rb.undoChange(false); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestInputFragmentation(t *testing.T) {
	input := "i世界\x1b[A\x1b[<0;10;2M\x1b[200~literal\x1b:q!\nα\x1b[201~\x1b[15;5~"
	for chunk := 1; chunk < 17; chunk++ {
		var p inputParser
		var got []keyEvent
		for offset := 0; offset < len(input); offset += chunk {
			p.feed([]byte(input[offset:min(len(input), offset+chunk)]), time.Now(), func(k keyEvent) { got = append(got, k) })
		}
		if len(got) != 7 || got[1].Key != "世" || got[3].Key != "<Up>" || got[4].Mouse == nil || got[5].Paste != "literal\x1b:q!\nα" || got[6].Key != "<C-F5>" {
			t.Fatalf("chunk%d: %+v", chunk, got)
		}
	}
	var p inputParser
	var got []keyEvent
	emit := func(k keyEvent) { got = append(got, k) }
	now := time.Now()
	p.feed([]byte("\x1b"), now, emit)
	p.expire(now.Add(time.Second), emit)
	if len(got) != 1 || got[0].Key != "<Esc>" {
		t.Fatal("Escape timeout")
	}
}

func TestRenderIncrementalResizeAndControlSafety(t *testing.T) {
	e := memoryEditor("α世界e\u0301\t\x1b[31m\nlast\n")
	defer e.close(false)
	var r renderer
	var out strings.Builder
	for _, size := range [][2]int{{24, 80}, {2, 8}, {10, 15}, {3, 8}} {
		e.rows, e.cols = size[0], size[1]
		if err := e.draw(&r, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "\x1b[31m") {
			t.Fatal("document control sequence reached terminal")
		}
		if !utf8.ValidString(out.String()) {
			t.Fatal("renderer broke UTF-8")
		}
		out.Reset()
		if err := e.draw(&r, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.String()) > 120 {
			t.Fatal("idle renderer rewrote unchanged text")
		}
	}
	e.execute("vsplit")
	e.execute("split")
	e.rows, e.cols = 2, 8
	if err := e.draw(&r, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestBulkSubstitutionAndInverse(t *testing.T) {
	e := memoryEditor(strings.Repeat("α β α\n", 3000))
	initial := editorText(e)
	e.execute("%s/α/long世界/g")
	changed := editorText(e)
	if changed == initial {
		t.Fatalf("bulk substitution failed: %s", e.message)
	}
	editKeys(e, "u")
	if editorText(e) != initial {
		t.Fatal("bulk inverse did not restore UTF-8 ranges")
	}
	editKeys(e, "<C-r>")
	if editorText(e) != changed {
		t.Fatal("bulk redo failed")
	}
	for _, command := range []string{"%s/α//g", "%s/α/X/g", "%s/α/XXX/g"} {
		e = memoryEditor("ααα\n")
		initial = editorText(e)
		e.execute(command)
		editKeys(e, "u")
		if editorText(e) != initial {
			t.Fatalf("inverse of %s: %q", command, editorText(e))
		}
	}
}

func TestSplitUndoClampsInactiveCursor(t *testing.T) {
	e := memoryEditor("a\n")
	editKeys(e, "A very long insertion<Esc>")
	e.splitWindow(true)
	editKeys(e, "$u<C-w>wix<Esc>")
	if e.pos() < 0 || e.pos() > len(e.buffer().text) {
		t.Fatal("split cursor escaped buffer after undo")
	}
}

func FuzzEditorState(f *testing.F) {
	f.Add([]byte("ihello\x1bjdwpu"))
	f.Add([]byte{0, 1, 255, 32, 18, 16})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			return
		}
		e := memoryEditor("one two\nα世界\n\nlast\n")
		keys := []string{"h", "j", "k", "l", "w", "b", "e", "0", "$", "gg", "G", "iX<Esc>", "a界<Esc>", "x", "dd", "u", "<C-r>", "p", "cwY<Esc>", "vly", "<C-v>jld", ">>", "<<", "J", "rQ"}
		for _, value := range data {
			editKeys(e, keys[int(value)%len(keys)])
			if len(e.buffer().text) > 65536 {
				return
			}
			if e.pos() < 0 || e.pos() > len(e.buffer().text) {
				t.Fatal("invalid cursor")
			}
		}
	})
}

func FuzzInput(f *testing.F) {
	f.Add([]byte("\x1b[<0;2;3Mhello世界"))
	f.Add([]byte("\x1b[200~x\x1b[201~"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		var p inputParser
		for at := 0; at < len(data); at += 7 {
			p.feed(data[at:min(len(data), at+7)], time.Now(), func(keyEvent) {})
		}
		p.expire(time.Now().Add(time.Second), func(keyEvent) {})
	})
}
