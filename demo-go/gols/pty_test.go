package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"st-go/ptyutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("GOLS_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestGoLSPTYSelectionPreviewAndVisibleContents(t *testing.T) {
	file := writeTestCorpus(t)
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	source := `\begin{document}\section{First topic}` + strings.Repeat("A paragraph of text for document navigation.\n\n", 20) +
		`\section{Second topic}The second section.\end{document}`
	_, updateErr := db.Exec(`UPDATE documents SET content=?, sha256='sections' WHERE id=2`, source)
	closeErr := db.Close()
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := ptyutil.SetWinSizePixels(master, 28, 100, 800, 448); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := ptyutil.Start(slave, []string{executable, file}, append(os.Environ(),
		"GOLS_TEST_PROCESS=1", "HOME="+t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("GoLS child did not exit")
		}
	})
	var mu sync.Mutex
	var output bytes.Buffer
	go func() {
		var buffer [8192]byte
		for {
			n, err := master.Read(buffer[:])
			if n > 0 {
				mu.Lock()
				output.Write(buffer[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	snapshot := func() string { mu.Lock(); defer mu.Unlock(); return output.String() }
	reset := func() { mu.Lock(); output.Reset(); mu.Unlock() }
	await := func(what string, ready func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ready(snapshot()) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("waiting for %s; output: %q", what, snapshot())
	}
	contains := func(text string) func(string) bool {
		return func(output string) bool { return strings.Contains(output, text) }
	}
	send := func(keys string) {
		t.Helper()
		if _, err := master.Write([]byte(keys)); err != nil {
			t.Fatal(err)
		}
	}
	await("database chooser", contains("Test corpus"))
	send("\r")
	await("corpus list", contains("Corpora in Test corpus"))
	send("\r")
	await("automatic selected-document preview", contains("fit-height page 1\x1b\\"))
	reset()
	send("\x1b[B") // Select Lecture one instead of the initial Macros fragment.
	await("lecture preview", func(output string) bool {
		return regexp.MustCompile(`TEX  1/\d+  Lecture one`).MatchString(output) && strings.Contains(output, "fit-height page 1")
	})
	placement := regexp.MustCompile("\x1bPopen '([^']+)' rect 1 3 100 25 fit-height page 1\x1b\\\\")
	match := placement.FindStringSubmatch(snapshot())
	if match == nil {
		t.Fatalf("no bounded fit-height placement: %q", snapshot())
	}
	link := match[1]
	selectedPDF, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	reset()
	send("\r")
	await("visible table of contents", func(output string) bool {
		return strings.Contains(output, "First topic") && strings.Contains(output, "Second topic") && strings.Contains(output, " SELECTED")
	})
	if target, err := os.Readlink(link); err != nil || target != selectedPDF {
		t.Fatalf("entering contents replaced the selected PDF: %q, %v", target, err)
	}
	reset()
	send("\x1b[F") // Last TOC section.
	await("second section selection", func(output string) bool {
		return strings.Contains(output, selectedStyle+" Second topic") && strings.Contains(output, "fit-height page ")
	})
	pagePattern := regexp.MustCompile(`fit-height page (\d+)`)
	pageMatch := pagePattern.FindStringSubmatch(snapshot())
	if pageMatch == nil {
		t.Fatal("TOC jump emitted no PDF page")
	}
	page, _ := strconv.Atoi(pageMatch[1])
	if page < 2 {
		t.Fatalf("second section did not jump beyond the opening page: %d", page)
	}
	reset()
	if err := ptyutil.SetWinSizePixels(master, 32, 120, 960, 512); err != nil {
		t.Fatal(err)
	}
	await("resized fit-height preview", contains("rect 1 3 120 29 fit-height page "))
	if target, err := os.Readlink(link); err != nil || target != selectedPDF {
		t.Fatalf("resize regenerated the opened PDF: %q, %v", target, err)
	}
	reset()
	send("\t")
	await("hidden navigation", contains("\x1b[?2026l"))
	if strings.Contains(snapshot(), " SELECTED") {
		t.Fatal("Tab did not hide navigation")
	}
	reset()
	send("\t")
	await("restored contents", contains("Second topic"))
	send("q")
	select {
	case <-done:
		if exitErr != nil {
			t.Fatal(exitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GoLS did not quit")
	}
	if _, err := os.Stat(filepath.Dir(selectedPDF)); !os.IsNotExist(err) {
		t.Fatalf("session preview directory was not removed: %v", err)
	}
}
