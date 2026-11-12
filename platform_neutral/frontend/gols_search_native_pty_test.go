//go:build webpcache && !backend_web && cgo && !purego

package frontend

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"st-go/ptyutil"
	"st-go/term"
)

func TestGoLSNativeDocumentSearchPTY(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "corpus.sqlite3")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE archive_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT INTO archive_meta VALUES('catalogue_title','Synthetic corpus');
CREATE TABLE documents(id INTEGER PRIMARY KEY,path TEXT,dir TEXT,name TEXT,extension TEXT,title TEXT,course TEXT,kind TEXT,status TEXT,visibility TEXT,sequence INTEGER,content TEXT,search_text TEXT,size INTEGER,mod_time_ns INTEGER,sha256 TEXT,indexed_at TEXT);
INSERT INTO documents VALUES(1,'AA/document.tex','AA','document.tex','.tex','Synthetic document','AA','lecture','current','user',1,?,'needle café alpha',100,1,'synthetic','now');`,
		`\begin{document}needle needle café α.`+strings.Repeat("\n\nA neutral filler paragraph for page navigation.", 8)+`\section{Later}needle\end{document}`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dir, "gols")
	build := exec.Command("go", "-C", "demo-go/gols", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gols:%v\n%s", err, output)
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
	trm := regionTestTerminal(t)
	trm.ensureFramebuffer(402, 226)
	trm.applyDisplayLayout(100, 28)
	trm.termCore.SetWriter(func(data []byte) { master.Write(data) })
	child, err := ptyutil.Start(slave, []string{binaryPath, file}, append(os.Environ(), "HOME="+dir))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	readerDone := make(chan struct{})
	var mu sync.Mutex
	go func() {
		defer close(readerDone)
		var pending []byte
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				mu.Lock()
				output.Write(buffer[:n])
				mu.Unlock()
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
		syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		child.Wait()
		master.Close()
		select {
		case <-readerDone:
		case <-time.After(2 * time.Second):
			t.Error("GoLS PTY reader did not stop")
		}
	}()
	var screen string
	await := func(what string, check func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			trm.flushRegionReplies()
			var lines []string
			for row := 0; row < trm.termCore.Rows(); row++ {
				lines = append(lines, trm.termCore.LineText(row))
			}
			screen = strings.Join(lines, "\n")
			ok := check(screen)
			trm.mu.Unlock()
			if ok {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		raw := output.String()
		mu.Unlock()
		if len(raw) > 16000 {
			raw = raw[len(raw)-16000:]
		}
		screen = strings.ReplaceAll(screen, string(term.ImageRune), "·")
		t.Fatalf("waiting for %s\nscreen=%q\noutput=%q", what, screen, raw)
	}
	send := func(keys string) {
		t.Helper()
		if _, err := master.Write([]byte(keys)); err != nil {
			t.Fatal(err)
		}
	}
	await("database chooser", func(screen string) bool { return strings.Contains(screen, "Synthetic corpus") })
	send("\r")
	await("courses", func(screen string) bool { return strings.Contains(screen, "Corpora in Synthetic corpus") })
	send("\r")
	await("document preview", func(screen string) bool { return strings.Contains(screen, "TEX  1/") })
	send("\r")
	await("contents", func(screen string) bool {
		return strings.Contains(screen, "Contents:") && strings.Contains(screen, "Document start")
	})
	visiblePixels := func() ([32]byte, int) {
		trm.mu.Lock()
		defer trm.mu.Unlock()
		hash := sha256.New()
		tinted := 0
		var encoded [4]byte
		for y := 2; y < trm.termCore.Rows()-1; y++ {
			for x := 0; x < trm.termCore.Cols(); x++ {
				glyph := trm.termCore.LineAt(x, y)
				if glyph.U != term.ImageRune {
					continue
				}
				offset := int(glyph.Fg)
				for _, pixel := range trm.imageAtlas[offset : offset+trm.cw*trm.ch] {
					binary.LittleEndian.PutUint32(encoded[:], pixel)
					hash.Write(encoded[:])
					r, g, b := (pixel>>16)&255, (pixel>>8)&255, pixel&255
					if r > g && g > b {
						tinted++
					}
				}
			}
		}
		var digest [32]byte
		copy(digest[:], hash.Sum(nil))
		return digest, tinted
	}
	send("/needle\r")
	await("first occurrence", func(screen string) bool { return strings.Contains(screen, "Match 1/3 on page") })
	first, tinted := visiblePixels()
	if tinted == 0 {
		t.Fatal("native match highlights did not reach visible PDF cells")
	}
	send("n")
	await("second occurrence", func(screen string) bool { return strings.Contains(screen, "Match 2/3 on page") })
	second, _ := visiblePixels()
	if second == first {
		t.Fatal("next occurrence did not change visible highlight pixels")
	}
	send("n")
	await("later-page occurrence", func(screen string) bool { return strings.Contains(screen, "Match 3/3 on page") })
	send("n")
	await("wrapped occurrence", func(screen string) bool { return strings.Contains(screen, "Match 1/3 on page") })
	send("N")
	await("previous occurrence", func(screen string) bool { return strings.Contains(screen, "Match 3/3 on page") })
	send("/{alpha}\r")
	await("Greek shortcut", func(screen string) bool { return strings.Contains(screen, "Match 1/1 on page") })
	send("/caf\\'e\r")
	await("accent shortcut", func(screen string) bool { return strings.Contains(screen, "Match 1/1 on page") })
	send(":noh\r")
	await("cleared highlights", func(screen string) bool { return strings.Contains(screen, "Search highlights cleared") })
	_, tinted = visiblePixels()
	if tinted != 0 {
		t.Fatal("clearing search left tinted document cells")
	}
	send("q")
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("search modified the read-only corpus")
	}
}
