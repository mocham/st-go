package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

var tabFrameRE = regexp.MustCompile("\x1bPtab-spawn auth ([0-9a-f]+) ([0-9a-f]{32}) ([A-Za-z0-9+/]+)\x1b\\\\")

func browserTabRequest(t *testing.T, f *fbTest) (string, api.TabSpawnRequest) {
	t.Helper()
	var id string
	var request api.TabSpawnRequest
	probed := make(map[string]bool)
	waitFor(t, f, func() bool {
		for _, match := range tabFrameRE.FindAllStringSubmatch(f.output(), -1) {
			data, err := base64.RawStdEncoding.DecodeString(match[3])
			if err != nil {
				t.Fatal(err)
			}
			var r api.TabSpawnRequest
			if err := json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.Probe {
				if !probed[match[2]] {
					probed[match[2]] = true
					f.send("\x1bPtab-spawn " + match[2] + " supported 1\x1b\\")
				}
				continue
			}
			id, request = match[2], r
			return true
		}
		return false
	}, "focused tab request")
	return id, request
}

func TestBrowserRequestsFocusedTabAndKeepsItsPTY(t *testing.T) {
	for _, backend := range []string{"x11", "fb", "web"} {
		t.Run(backend, func(t *testing.T) {
			dir, tools := t.TempDir(), documentToolTestPath(t)
			name := "a '; 世界.txt"
			writeFiles(t, dir, name)
			marker := filepath.Join(tools, "opened")
			writeDocumentTool(t, tools, "govim", `printf 'local' > "$MARKER"`)
			env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_BACKEND="+backend, "ST_GO_TABS=1", "ST_GO_TAB_TOKEN="+strings.Repeat("ab", 16)), "ST_FILE_BROWSER_OPEN")
			f := startBrowser(t, env, dir)
			before := len(f.output())
			f.send("\x1b[B\r")
			id, request := browserTabRequest(t, f)
			if request.Detached || request.Directory != dir || len(request.Command) != 2 || request.Command[0] != filepath.Join(tools, "govim") || request.Command[1] != filepath.Join(dir, name) {
				t.Fatal("incorrect literal request", request)
			}
			if strings.Contains(f.output()[before:], "\x1b[?1049l") || strings.Contains(f.output()[before:], "place bottom-left") {
				t.Fatal("tab request handed away or collapsed the browser terminal")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("browser launched a local duplicate")
			}
			f.send("\x1bPtab-spawn " + id + " accepted 3\x1b\\")
			waitContains(t, f, "Opened tab:", "spawn acknowledgement")
			// Unknown/late replies in modal path entry are drained, not typed into it.
			f.send("/")
			f.send("\x1bPtab-spawn " + id + " accepted 3\x1b\\\x03r")
			waitContains(t, f, "Refreshed", "browser input after focused spawn")
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("accepted request fell back locally")
			}
		})
	}
}

func TestRejectedTabFallsBackToExistingTerminalTool(t *testing.T) {
	dir, tools := t.TempDir(), documentToolTestPath(t)
	writeFiles(t, dir, "a.txt")
	marker, created := filepath.Join(tools, "opened"), filepath.Join(dir, "created")
	writeDocumentTool(t, tools, "govim", interactiveDocumentTool)
	env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "CREATED="+created, "ST_GO_TABS=1", "ST_GO_TAB_TOKEN="+strings.Repeat("ab", 16)), "ST_FILE_BROWSER_OPEN")
	f := startBrowser(t, env, dir)
	f.send("\x1b[B\r")
	id, _ := browserTabRequest(t, f)
	f.send("\x1bPtab-spawn " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("disabled")) + "\x1b\\")
	waitContains(t, f, "DOCUMENT_TOOL_READY", "fallback tool owns browser PTY")
	waitFileContent(t, marker, "govim|1|"+filepath.Join(dir, "a.txt")+"|"+dir)
	f.send("q")
	waitContains(t, f, "govim finished", "fallback return")
	f.send("\x1bPtab-spawn " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("late")) + "\x1b\\r")
	waitContains(t, f, "Refreshed", "late reply was not treated as keys")
}

func TestDisabledTabCapabilityUsesCurrentBehavior(t *testing.T) {
	for _, capability := range []string{"0", ""} {
		t.Run(capability, func(t *testing.T) {
			dir, tools := t.TempDir(), documentToolTestPath(t)
			writeFiles(t, dir, "a.txt")
			marker := filepath.Join(tools, "opened")
			writeDocumentTool(t, tools, "govim", `printf '%s' "$1" > "$MARKER"`)
			env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_TABS="+capability, "ST_GO_TAB_TOKEN="+strings.Repeat("ab", 16)), "ST_FILE_BROWSER_OPEN")
			f := startBrowser(t, env, dir)
			f.send("\x1b[B\r")
			waitFileContent(t, marker, filepath.Join(dir, "a.txt"))
			if strings.Contains(f.output(), "tab-spawn auth") {
				t.Fatal("unsupported terminal received a tab request")
			}
		})
	}
}

func TestTabTimeoutRetainsPendingRequestWithoutDuplicateFallback(t *testing.T) {
	var out bytes.Buffer
	b := &Browser{out: bufio.NewWriter(&out), running: true, pendingTabs: map[string]pendingTab{"id": {deadline: time.Now().Add(-time.Second)}}}
	b.pollTabRequests()
	if len(b.pendingTabs) != 1 || !b.pendingTabs["id"].deadline.IsZero() || len(b.tabFallbacks) != 0 {
		t.Fatal("ambiguous timeout was retried or discarded")
	}
}

func TestNonrespondingTabProbeFallsBackWithoutSendingSpawn(t *testing.T) {
	dir, tools := t.TempDir(), documentToolTestPath(t)
	writeFiles(t, dir, "a.txt")
	marker := filepath.Join(tools, "opened")
	writeDocumentTool(t, tools, "govim", `printf '%s' "$1" > "$MARKER"`)
	env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_TABS=1", "ST_GO_TAB_TOKEN="+strings.Repeat("ab", 16)), "ST_FILE_BROWSER_OPEN")
	f := startBrowser(t, env, dir)
	f.send("\x1b[B\r")
	waitFileContent(t, marker, filepath.Join(dir, "a.txt"))
	matches := tabFrameRE.FindAllStringSubmatch(f.output(), -1)
	if len(matches) != 1 {
		t.Fatal("unsupported host was retried", len(matches))
	}
	data, _ := base64.RawStdEncoding.DecodeString(matches[0][3])
	var request api.TabSpawnRequest
	if json.Unmarshal(data, &request) != nil || !request.Probe || len(request.Command) != 0 {
		t.Fatal("an unconfirmed host received an executable spawn", request)
	}
}

func TestArchiveTabRequestStagesFileUntilHostAcknowledgement(t *testing.T) {
	t.Setenv("ST_GO_TABS", "1")
	t.Setenv("ST_GO_TAB_TOKEN", strings.Repeat("ab", 16))
	dir := t.TempDir()
	source := filepath.Join(dir, "archive-entry.txt")
	if err := os.WriteFile(source, []byte("archive contents"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	b := &Browser{out: bufio.NewWriter(&output), fd: -1, dir: dir, cacheDir: t.TempDir(), archiveStack: []*archiveCtx{{}}}
	if !b.requestTerminalTab("/bin/govim", source) {
		t.Fatal("archive tab request was not sent")
	}
	var probeID string
	for id := range b.pendingTabs {
		probeID = id
	}
	b.acceptTabReply("tab-spawn " + probeID + " supported 1")
	var spawnID string
	var pending pendingTab
	for id, request := range b.pendingTabs {
		spawnID, pending = id, request
	}
	if pending.probe || pending.request.CopyFile == nil || *pending.request.CopyFile != 1 || pending.request.Command[1] == source || pending.path != source {
		t.Fatal("archive ownership handoff was not requested", pending.request)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(pending.request.Command[1]); err != nil || string(data) != "archive contents" {
		t.Fatal("archive navigation invalidated the pending spawn", err)
	}
	b.acceptTabReply("tab-spawn " + spawnID + " accepted 3")
	if _, err := os.Stat(pending.transfer); !os.IsNotExist(err) {
		t.Fatal("accepted handoff left a browser-owned transfer directory", err)
	}
}
