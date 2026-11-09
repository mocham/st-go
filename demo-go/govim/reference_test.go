package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Vim is an optional test oracle only. govim never invokes it at runtime.
func TestEditingAgainstVimReference(t *testing.T) {
	vim, err := exec.LookPath("vim")
	if err != nil {
		t.Skip("optional Vim reference executable is unavailable")
	}
	initial := "one two three\n  alpha beta\n\n(a [b c] d)\nlast line\n"
	for _, keys := range []string{
		"dw", "d2w", "2d3w", "cwX<Esc>", "2cwX<Esc>", "d$", "D", "jccX<Esc>", "j2ccX<Esc>", "Gdd", "jddp", "yyp", "2yyP", "ylp", "$2X", "j0d^", "j^d0", "jdG", "Gdgg", "llvld", "Vjd", "2>>", ">G", "gUw", "guG", "g~w", "3~", "l2rX", "Rxy<BS>z<Esc>", "iXX<Esc>u<C-r>", "cwX<Esc>w.", "A!<Esc>j.", "qaAx<Esc>jq2@a", "jdiw", "jdaw", "3jldi(", "3j2ldi[", "3jda(", "3jdi[", "3j%dh", "J", "gJ", "oX<Esc>", "OX<Esc>", "3oX<Esc>", "j^aw<Esc>", "0ttrX", "0ftrX", "$FtrX", "ftrX;rZ", "ttrX;rZ", "w<C-a>",
		"$dFt", "$dTt", "dft", "dtt", "jmaGd'a", "jmaGd`a", "/two<CR>ggdn", "d/three<CR>", "/two<CR>ggcnX<Esc>",
	} {
		t.Run(keys, func(t *testing.T) {
			dir := t.TempDir()
			if strings.HasPrefix(keys, "qa") {
				t.Skip(":normal cannot record Vim macros; native macro tests cover this")
			}
			file := filepath.Join(dir, "input.txt")
			if err := os.WriteFile(file, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			var command strings.Builder
			for _, event := range parseKeyNotation(keys) {
				key := event.Key
				if strings.HasPrefix(key, "<") && strings.HasSuffix(key, ">") {
					if key == "<Enter>" {
						key = "<CR>"
					}
					command.WriteByte('\\')
					command.WriteString(key)
				} else {
					for _, r := range key {
						switch {
						case r == '\\' || r == '"':
							command.WriteByte('\\')
							command.WriteRune(r)
						case r < 32:
							fmt.Fprintf(&command, "\\x%02x", r)
						default:
							command.WriteRune(r)
						}
					}
				}
			}
			script := "set nocompatible nofixeol backspace=indent,eol,start autoindent smartindent shiftwidth=4 tabstop=4 expandtab\nnormal! gg0\nsilent! execute \"normal! " + command.String() + "\"\nwq!\n"
			cmd := exec.Command(vim, "-Nu", "NONE", "-U", "NONE", "-i", "NONE", "-n", "-es", file)
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "HOME="+dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				if _, ok := err.(*exec.ExitError); !ok {
					t.Fatalf("Vim oracle: %v %q", err, output)
				}
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			e := memoryEditor(initial)
			editKeys(e, keys)
			if got := editorText(e); got != string(want) {
				t.Fatalf("govim=%q\nVim  =%q\nmode=%d pos=%d message=%q", got, want, e.mode, e.pos(), e.message)
			}
		})
	}
}
