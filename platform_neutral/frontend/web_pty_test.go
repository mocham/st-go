package frontend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestWebBackendPTYDirectoryInputAndImagePreview(t *testing.T) {
	path, err := filepath.Abs("BINARIES/st-web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Skip("build with make st-web")
	}
	dir := t.TempDir()
	cmd := exec.Command(path, "-port", "0", "-dir", dir, "-e", "/bin/sh")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	urls := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Web terminal: ") {
				urls <- strings.TrimPrefix(scanner.Text(), "Web terminal: ")
				return
			}
		}
	}()
	var address string
	select {
	case address = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("web server did not start")
	}
	origin := address[:strings.Index(address[7:], "/")+7]
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(address, "http")+"ws", "", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello struct{ Type string }
	if err := websocket.JSON.Receive(conn, &hello); err != nil || hello.Type != "hello" {
		t.Fatalf("hello: %v", err)
	}
	send := func(text string) {
		t.Helper()
		if err := websocket.JSON.Send(conn, map[string]any{"type": "text", "text": text}); err != nil {
			t.Fatal(err)
		}
		if err := websocket.JSON.Send(conn, map[string]any{"type": "key", "key": "Enter"}); err != nil {
			t.Fatal(err)
		}
	}
	send("pwd > .web-cwd; printf '\\033]0;IGNORED_TITLE\\007'")
	deadline := time.Now().Add(4 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, ".web-cwd"))
		if err == nil && strings.TrimSpace(string(data)) == dir {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PTY did not start in requested directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 34, G: 187, B: 68, A: 255})
		}
	}
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preview.png"), encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	send(`printf "\033Popen 'preview.png' rect 1 3 2 2 fit-contain\033\\"`)
	for {
		var data []byte
		if err := websocket.Message.Receive(conn, &data); err != nil {
			t.Fatalf("no rendered image frame: %v", err)
		}
		var scene struct {
			Scenes []struct {
				Update struct {
					Rows []struct{ Media []struct{ ID uint64 } }
				}
			}
		}
		if json.Unmarshal(data, &scene) != nil {
			continue
		}
		for _, s := range scene.Scenes {
			for _, row := range s.Update.Rows {
				for _, m := range row.Media {
					response, err := http.Get(address + "media/" + strconv.FormatUint(m.ID, 10))
					if err != nil {
						t.Fatal(err)
					}
					original, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil || !bytes.Equal(original, encoded.Bytes()) {
						t.Fatal("media resource did not preserve original encoded PNG", err)
					}
					return
				}
			}
		}
	}
}
