package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
)

func previewPTYCommands(output string) []string {
	var commands []string
	for {
		start := strings.Index(output, "\x1bP")
		if start < 0 {
			break
		}
		output = output[start+2:]
		end := strings.Index(output, "\x1b\\")
		if end < 0 {
			break
		}
		commands = append(commands, output[:end])
		output = output[end+2:]
	}
	return commands
}

func previewPTYOpenCommands(p *ptyEditor) []string {
	var commands []string
	for _, command := range previewPTYCommands(p.text()) {
		if strings.HasPrefix(command, "region secondary open ") {
			commands = append(commands, command)
		}
	}
	return commands
}

func TestPTYGovimAutomaticPreviewSaveNavigationAndCleanup(t *testing.T) {
	for _, name := range []string{"notes.tex", "notes.md"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			source := "# Synthetic heading\n\nFirst paragraph.\n\nSecond paragraph.\n"
			if strings.HasSuffix(name, ".tex") {
				source = "\\begin{document}\n\\section{Synthetic heading}\nFirst paragraph.\nSecond paragraph.\n\\end{document}\n"
			}
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			p := startPTYEditor(t, "--preview-gap", "0", path)
			p.await(t, "preview region query", func() bool {
				return strings.Contains(p.text(), "region split 60%; region secondary scroll report; region query")
			})
			if err := ptyutil.SetWinSize(p.master, 24, 32); err != nil {
				t.Fatal(err)
			}
			p.send(t, "\x1bPregion layout main 1 1 32 24 secondary 33 1 48 24\x1b\\")
			p.await(t, "initial in-process PDF", func() bool { return len(previewPTYOpenCommands(p)) == 1 })
			open := previewPTYOpenCommands(p)[0]
			pdfPath, _, ok := strings.Cut(strings.TrimPrefix(open, "region secondary open '"), "' background ")
			if !ok || !strings.HasSuffix(open, "fit-width continuous") {
				t.Fatalf("bad preview command: %q", open)
			}
			pdf, err := os.ReadFile(pdfPath)
			if err != nil || !strings.HasPrefix(string(pdf), "%PDF-") {
				t.Fatalf("missing preview PDF %q: %v", pdfPath, err)
			}
			p.send(t, "ggA saved without moving\x1b")
			time.Sleep(100 * time.Millisecond)
			before := len(p.text())
			p.send(t, ":wnomove\r")
			p.await(t, "wnomove PDF refresh", func() bool { return len(previewPTYOpenCommands(p)) == 2 })
			for _, command := range previewPTYCommands(p.text()[before:]) {
				if strings.HasPrefix(command, "region secondary position ") {
					t.Fatalf("wnomove navigated: %s", command)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "saved without moving") {
				t.Fatalf("save hook did not write document: %q %v", data, err)
			}
			before = len(p.text())
			p.send(t, "G:w\r")
			p.await(t, "unchanged save cursor navigation", func() bool { return strings.Contains(p.text()[before:], "region secondary position ") })
			if len(previewPTYOpenCommands(p)) != 2 {
				t.Fatal("unchanged save regenerated the PDF")
			}
			// A framed seek is not editor keystrokes; normal editing continues.
			p.send(t, "\x1bPregion secondary event seek 2\x1b\\")
			p.send(t, ":q!\r")
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Fatal("preview session did not exit")
			}
			if p.err != nil {
				t.Fatalf("govim exit: %v output=%q", p.err, p.text())
			}
			p.await(t, "restored full-width region", func() bool { return strings.Contains(p.text(), "\x1bPregion reset\x1b\\") })
			if _, err := os.Stat(filepath.Dir(pdfPath)); !os.IsNotExist(err) {
				t.Fatal("preview session left temporary PDF files")
			}
		})
	}
}

func TestPTYPreviewUnsupportedTerminalKeepsEditorUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("# Original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, path)
	p.await(t, "unsupported region fallback", func() bool { return strings.Contains(p.text(), "preview unavailable") })
	p.send(t, "A changed\x1b")
	time.Sleep(100 * time.Millisecond)
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("unsupported terminal blocked editor exit")
	}
	if p.err != nil {
		t.Fatalf("editor exit: %v", p.err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "# Original changed\n" {
		t.Fatalf("editor save damaged after fallback: %q %v", data, err)
	}
}

func TestPreviewRefusesFIFOIncludesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "main.tex")
	fifo := filepath.Join(dir, "blocked.tex")
	if err := os.WriteFile(root, []byte("\\input{blocked}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan previewResult, 1)
	go func() { done <- renderPreview(previewRequest{path: root}, nil) }()
	select {
	case result := <-done:
		if result.err == nil {
			t.Fatal("non-regular include accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("preview blocked opening a FIFO include")
	}
}
