package frontend

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/ptyutil"
)

// Run the real zat client against the terminal parser, native PDF worker, and
// PTY resize/reply path, without substituting canned metadata replies or X11.
// ST_TEST_ZAT_PDF optionally selects a local regression document; it is never
// copied into the repository. ST_TEST_ZAT_TEXT selects text expected per page.
func TestZatNativePageTextPTY(t *testing.T) {
	file, expected := os.Getenv("ST_TEST_ZAT_PDF"), os.Getenv("ST_TEST_ZAT_TEXT")
	generated := file == ""
	if file != "" && expected == "" {
		t.Fatal("ST_TEST_ZAT_PDF requires ST_TEST_ZAT_TEXT")
	}
	if file == "" {
		file = filepath.Join(t.TempDir(), "document.pdf")
		if err := os.WriteFile(file, metadataTestPDF("Opening"), 0600); err != nil {
			t.Fatal(err)
		}
		expected = "Selectable"
	}
	file, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "zat")
	build := exec.Command("go", "-C", "demo-go/zat", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build zat: %v\n%s", err, output)
	}
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := ptyutil.SetWinSizePixels(master, 24, 80, 320, 192); err != nil {
		t.Fatal(err)
	}
	trm := regionTestTerminal(t)
	ensureFramebuffer(322, 194)
	trm.applyDisplayLayout(80, 24)
	trm.ttyResize = func(rows, cols int) { ptyutil.SetWinSizePixels(master, rows, cols, cols*trm.cw, rows*trm.ch) }
	var replies, output bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { replies.Write(data); master.Write(data) })
	child, err := ptyutil.Start(slave, []string{binary, file}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var pending []byte
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				trm.mu.Lock()
				output.Write(buffer[:n])
				trm.mu.Unlock()
				pending = append(pending, buffer[:n]...)
				consumed := trm.writeTerminalOutput(trm.termCore, pending)
				pending = pending[consumed:]
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { master.Close(); <-readerDone }()
	await := func(description string, predicate func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		var screen string
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			trm.flushRegionReplies()
			var lines []string
			for row := 0; row < trm.termCore.Rows(); row++ {
				lines = append(lines, trm.termCore.LineText(row))
			}
			screen = strings.Join(lines, "\n")
			trm.mu.Unlock()
			if predicate(screen) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		trm.mu.Lock()
		raw, frames := output.String(), replies.String()
		trm.mu.Unlock()
		t.Fatalf("waiting for %s\nscreen=%q\noutput=%q\nreplies=%q", description, screen, raw, frames)
	}
	await("PDF ready", func(screen string) bool {
		return strings.Contains(screen, "1/") && !strings.Contains(screen, "1/0") && !strings.Contains(screen, "loading")
	})
	master.Write([]byte("\x1b[Z"))
	await("extracted page text", func(screen string) bool {
		return strings.Contains(screen, "PAGE 1 TEXT") && strings.Contains(screen, expected)
	})
	// Small preview widths can fit multiple pages; zoom makes every page
	// (including the last) independently alignable at the top of this viewport.
	fmt.Fprint(master, ":zoom 500\r")
	await("zoomed PDF", func(screen string) bool { return strings.Contains(screen, "[500%]") })
	trm.mu.Lock()
	trm.displayRegions.mu.Lock()
	pages := len(trm.displayRegions.secondary.bitmap.pageTops) - 1
	trm.displayRegions.mu.Unlock()
	trm.mu.Unlock()
	for page := 2; page <= pages; page++ {
		fmt.Fprintf(master, ":page %d\r", page)
		want := expected
		if generated {
			want = "No text returned"
		} // The fixture's second page is blank.
		await(fmt.Sprintf("page %d text", page), func(screen string) bool {
			return strings.Contains(screen, fmt.Sprintf("PAGE %d TEXT", page)) && strings.Contains(screen, want)
		})
	}
	trm.mu.Lock()
	if !strings.Contains(output.String(), "region secondary text ") {
		t.Error("zat never requested page text")
	}
	trm.mu.Unlock()
	fmt.Fprint(master, "q")
}
