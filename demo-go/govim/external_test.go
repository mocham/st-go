package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTexMainDirectives(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main ';$.tex")
	os.WriteFile(main, []byte("\\documentclass{article}\n\\begin{document}\nSynthetic text\n\\end{document}\n"), 0600)
	fragment := filepath.Join(dir, "fragment.tex")
	os.WriteFile(fragment, []byte("%main=main ';$.tex\nSynthetic fragment\n"), 0600)
	for _, path := range []string{main, fragment, fragment[:len(fragment)-4]} {
		got, err := texMain(path)
		if err != nil || got != main {
			t.Fatalf("%%main= resolution: %q %v", got, err)
		}
	}
	loop := filepath.Join(dir, "loop.tex")
	os.WriteFile(loop, []byte("%main=loop.tex\n"), 0600)
	if _, err := texMain(loop); err == nil {
		t.Fatal("cyclic TeX directive accepted")
	}
	comment := filepath.Join(dir, "comment.tex")
	os.WriteFile(comment, []byte("% \\begin{document}\n"), 0600)
	if _, err := texMain(comment); err == nil {
		t.Fatal("commented document marker accepted")
	}
}
