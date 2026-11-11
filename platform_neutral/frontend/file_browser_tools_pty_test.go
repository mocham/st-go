package frontend

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"st-go/ptyutil"
)

// Exercise real browser -> zat/govim -> browser handoffs against the terminal's
// parser, native PDF worker, region negotiation, and controlling PTY.
func TestFileBrowserNativeDocumentToolsPTY(t *testing.T) {
	tools := t.TempDir()
	for _, name := range []string{"zat", "govim"} {
		build := exec.Command("go", "-C", "demo-go/"+name, "build", "-o", filepath.Join(tools, name), ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
	}
	browser := filepath.Join(tools, "file-browser")
	build := exec.Command("go", "-C", "demo-go/file-browser", "build", "-o", browser, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build browser: %v\n%s", err, output)
	}
	for _, implementation := range []string{"go", "bash"} {
		for _, extension := range []string{"pdf", "tex", "md", "sh", "txt"} {
			t.Run(implementation+"/"+extension, func(t *testing.T) {
				dir := t.TempDir()
				var document []byte
				switch extension {
				case "pdf":
					document = metadataTestPDF("Synthetic bookmark")
				case "tex":
					document = []byte("\\documentclass{article}\n\\begin{document}\nSynthetic browser handoff.\n\\end{document}\n")
				case "md":
					document = []byte("# Synthetic browser handoff\n\nA **Markdown** document with $x^2$.\n")
				case "sh":
					document = []byte("#!/bin/sh\nprintf 'Synthetic browser handoff\\n'\n")
				case "txt":
					document = []byte("Synthetic browser handoff.\n")
				}
				if err := os.WriteFile(filepath.Join(dir, "document."+extension), document, 0600); err != nil {
					t.Fatal(err)
				}
				program := []string{browser, dir}
				if implementation == "bash" {
					program = []string{"bash", "demo/file-browser.sh", dir}
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
				trm.ensureFramebuffer(322, 194)
				trm.applyDisplayLayout(80, 24)
				trm.ttyResize = func(rows, cols int) { ptyutil.SetWinSizePixels(master, rows, cols, cols*trm.cw, rows*trm.ch) }
				trm.termCore.SetWriter(func(data []byte) { master.Write(data) })
				env := append(envWithout(os.Environ(), "ST_FILE_BROWSER_OPEN"), "PATH="+tools+":"+os.Getenv("PATH"))
				child, err := ptyutil.Start(slave, program, env)
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
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
				defer func() {
					// Stop the entire test-owned foreground group before joining
					// the reader: close alone cannot interrupt a blocking PTY read.
					_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
					child.Wait()
					master.Close()
					select {
					case <-readerDone:
					case <-time.After(2 * time.Second):
						t.Error("PTY reader did not stop")
					}
				}()
				await := func(description string, check func(string, string, bool) bool) {
					t.Helper()
					deadline := time.Now().Add(8 * time.Second)
					var screen, raw string
					for time.Now().Before(deadline) {
						trm.mu.Lock()
						trm.flushRegionReplies()
						var lines []string
						for row := 0; row < trm.termCore.Rows(); row++ {
							lines = append(lines, trm.termCore.LineText(row))
						}
						screen, raw = strings.Join(lines, "\n"), output.String()
						trm.displayRegions.mu.Lock()
						surface := trm.displayRegions.secondary
						previewReady := surface != nil && !surface.loading && len(surface.bitmap.pixels) > 0
						trm.displayRegions.mu.Unlock()
						ok := check(screen, raw, previewReady)
						trm.mu.Unlock()
						if ok {
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
					t.Fatalf("waiting for %s\nscreen=%q\noutput=%q", description, screen, raw)
				}
				await("browser", func(screen, _ string, _ bool) bool {
					return strings.Contains(screen, "st-go files") && strings.Contains(screen, "Ready")
				})
				fmt.Fprint(master, "\x1b[B\r")
				await("real document tool (and preview for document formats)", func(_, raw string, ready bool) bool {
					if extension == "sh" || extension == "txt" {
						return !ready && strings.Contains(raw, "\x1b[2 q")
					}
					return ready && strings.Contains(raw, "region secondary open ")
				})
				if extension == "pdf" {
					fmt.Fprint(master, "q")
				} else {
					fmt.Fprint(master, "\x1b:q!\r")
				}
				await("restored browser", func(screen, raw string, ready bool) bool {
					// Region reset can deliver a late resize after the first browser
					// frame, replacing its completion message with "Resized".
					return strings.Contains(screen, "st-go files") && !ready && strings.Contains(raw, "region reset") &&
						!strings.Contains(raw, "place bottom-left") && trm.termCore.Cols() == 80 && trm.termCore.Rows() == 24
				})
				fmt.Fprint(master, "r")
				await("browser input after tool exit", func(screen, _ string, _ bool) bool { return strings.Contains(screen, "Refreshed") })
				fmt.Fprint(master, "q")
			})
		}
	}
}
