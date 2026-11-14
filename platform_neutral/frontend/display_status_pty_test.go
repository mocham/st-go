package frontend

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"st-go/platform_neutral/api"
	"st-go/term"
)

func TestWebRegionStatusPTY(t *testing.T) {
	binary, err := filepath.Abs("BINARIES/st-web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skip("build with make st-web")
	}
	cmd := exec.Command(binary, "-port", "0", "-no-tabs", "-dir", t.TempDir(), "-e", "/bin/sh")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addresses := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if address, ok := strings.CutPrefix(scanner.Text(), "Web terminal: "); ok {
				addresses <- address
			}
		}
	}()
	var address string
	select {
	case address = <-addresses:
	case <-time.After(5 * time.Second):
		t.Fatal("web terminal did not start")
	}
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := websocket.Dial("ws://"+u.Host+u.Path+"ws", "", u.Scheme+"://"+u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello struct {
		Type                          string
		CellWidth, CellHeight, Border int
	}
	if err := websocket.JSON.Receive(conn, &hello); err != nil || hello.Type != "hello" {
		t.Fatalf("hello: %v", err)
	}
	send := func(value any) {
		t.Helper()
		if err := websocket.JSON.Send(conn, value); err != nil {
			t.Fatal(err)
		}
	}
	command := func(value string) {
		t.Helper()
		send(map[string]string{"type": "text", "text": "printf '%b' '\\033P" + value + "\\033\\\\'\n"})
	}
	type scene struct {
		ID                  uint64
		X, Y, Width, Height int
		Hidden              bool
		Update              api.SceneUpdate
	}
	await := func(description string, check func(scene) bool) scene {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			var message struct{ Scenes []scene }
			if err := websocket.JSON.Receive(conn, &message); err != nil {
				t.Fatalf("%s: %v", description, err)
			}
			for _, s := range message.Scenes {
				if check(s) {
					return s
				}
			}
		}
	}
	content := term.RegionStatus{Items: []term.RegionStatusItem{
		{Text: "11111111 Initial", Color: 0xffaaaaaa},
		{Text: "22222222 Processing", Color: 0xffffd75f},
		{Text: "33333333 Ready", Color: 0xff5fd787},
		{Text: "44444444 Inserted", Color: 0xffffffff},
	}}
	encoded := func() string { data, _ := json.Marshal(content); return base64.StdEncoding.EncodeToString(data) }
	send(map[string]any{"type": "resize", "width": 120*hello.CellWidth + 2*hello.Border, "height": 30*hello.CellHeight + 2*hello.Border})
	command("region split 50%; region secondary status " + encoded())
	footer := await("all colored slots reach browser", func(s scene) bool {
		if s.Hidden || s.Update.Background != regionStatusBackground {
			return false
		}
		found := map[string]uint32{}
		for _, row := range s.Update.Rows {
			for _, run := range row.Text {
				found[run.Text] = run.Foreground
			}
		}
		for _, item := range content.Items {
			if found[item.Text] != item.Color {
				return false
			}
		}
		return true
	})
	if len(footer.Update.Rows) != 2 {
		t.Fatalf("expected two columns on each of two rows, got %d rows", len(footer.Update.Rows))
	}
	content.Items = content.Items[2:]
	command("region secondary status " + encoded())
	footer = await("deleted slots disappear", func(s scene) bool {
		if s.ID != footer.ID || s.Hidden || !s.Update.Reset {
			return false
		}
		var runs []api.TextRun
		for _, row := range s.Update.Rows {
			runs = append(runs, row.Text...)
		}
		return len(runs) == 2 && runs[0].Text == content.Items[0].Text && runs[1].Text == content.Items[1].Text
	})
	oldWidth := footer.Width
	send(map[string]any{"type": "resize", "width": 100*hello.CellWidth + 2*hello.Border, "height": 24*hello.CellHeight + 2*hello.Border})
	footer = await("footer follows resize", func(s scene) bool {
		return s.ID == footer.ID && !s.Hidden && s.Width != oldWidth && len(s.Update.Rows) > 0
	})
	command("region secondary status clear")
	await("empty footer removed", func(s scene) bool { return s.ID == footer.ID && s.Hidden })
}
