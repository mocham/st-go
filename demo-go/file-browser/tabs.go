package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
	tabprotocol "st-go/tui/tabs"
)

type pendingTab struct {
	tool, path, directory, name string
	transfer                    string
	deadline                    time.Time
	probe                       bool
	request                     api.TabSpawnRequest
}

// An absent/disabled capability follows the existing same-terminal opener
// immediately. An accepted request never shares the browser PTY or input loop.
func (b *Browser) requestTerminalTab(tool, path string) bool {
	token := os.Getenv("ST_GO_TAB_TOKEN")
	if b.tabsUnavailable || os.Getenv("ST_GO_TABS") != "1" || !tabprotocol.Capability(token) {
		return false
	}
	if len(b.pendingTabs) > 0 {
		b.status = "Waiting for terminal tab requests"
		b.drawStatus()
		b.flush()
		return true
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false
	}
	id := hex.EncodeToString(nonce[:])
	request := api.TabSpawnRequest{Command: []string{tool, path}, Directory: b.dir, Title: displayText(baseName(path)), Detached: false}
	pending := pendingTab{tool: tool, path: path, directory: b.dir, name: baseName(path), deadline: time.Now().Add(5 * time.Second)}
	// Archive extraction directories can disappear on navigation. Stage the
	// argument outside that archive, then ask st-go to take its own copy before
	// acknowledging. Browser exit after acceptance cannot remove the tab's file.
	if b.isArchiveMode() {
		data, err := limits.ReadFile(path)
		if err != nil {
			return false
		}
		dir, err := os.MkdirTemp(b.cacheDir, "tab-transfer-")
		if err != nil {
			return false
		}
		staged := filepath.Join(dir, filepath.Base(path))
		if err := os.WriteFile(staged, data, 0600); err != nil {
			_ = os.RemoveAll(dir)
			return false
		}
		pending.transfer = dir
		request.Command[1] = staged
		index := 1
		request.CopyFile = &index
	}
	pending.request = request
	if !b.tabsVerified {
		request = api.TabSpawnRequest{Probe: true}
		pending.probe = true
		pending.deadline = time.Now().Add(750 * time.Millisecond)
	}
	frame, err := tabprotocol.RequestFrame(token, id, request)
	if err != nil {
		if pending.transfer != "" {
			_ = os.RemoveAll(pending.transfer)
		}
		return false
	}
	if b.pendingTabs == nil {
		b.pendingTabs = make(map[string]pendingTab)
	}
	b.pendingTabs[id] = pending
	fmt.Fprint(b.out, frame)
	b.status = "Opening tab: " + displayText(pending.name)
	b.drawStatus()
	b.flush()
	return true
}

func (b *Browser) acceptTabReply(control string) bool {
	reply, ok := tabprotocol.ParseReply(control)
	if !ok {
		return false
	}
	pending, ok := b.pendingTabs[reply.ID]
	if !ok {
		return true
	}
	if pending.probe && reply.Status == "supported" {
		delete(b.pendingTabs, reply.ID)
		b.tabsVerified = true
		var nonce [16]byte
		_, err := rand.Read(nonce[:])
		id := hex.EncodeToString(nonce[:])
		var frame string
		if err == nil {
			frame, err = tabprotocol.RequestFrame(os.Getenv("ST_GO_TAB_TOKEN"), id, pending.request)
		}
		if err != nil {
			b.tabFallbacks = append(b.tabFallbacks, pending)
			return true
		}
		pending.probe = false
		pending.deadline = time.Now().Add(5 * time.Second)
		b.pendingTabs[id] = pending
		fmt.Fprint(b.out, frame)
		b.flush()
		return true
	}
	if pending.probe && reply.Status != "error" {
		return true
	}
	if reply.Status != "accepted" && reply.Status != "error" {
		return true
	}
	delete(b.pendingTabs, reply.ID)
	if reply.Status == "accepted" {
		if pending.transfer != "" {
			_ = os.RemoveAll(pending.transfer)
		}
		b.status = "Opened tab: " + displayText(pending.name)
	} else {
		// A negative acknowledgement is sent only before a successful spawn;
		// therefore the old in-terminal fallback cannot launch a duplicate.
		b.tabFallbacks = append(b.tabFallbacks, pending)
		b.status = "Terminal tab unavailable; opening in this terminal"
	}
	if !b.promptActive && !b.manualActive && !b.mp3Active {
		b.drawStatus()
		b.flush()
	}
	return true
}

func (b *Browser) pollTabRequests() {
	for id, pending := range b.pendingTabs {
		if pending.deadline.IsZero() || time.Now().Before(pending.deadline) {
			continue
		}
		if pending.probe {
			// A probe cannot start a process, so even stale inherited metadata
			// or an older/non-responding host can safely use the old opener.
			delete(b.pendingTabs, id)
			b.tabsUnavailable = true
			b.tabFallbacks = append(b.tabFallbacks, pending)
			continue
		}
		pending.deadline = time.Time{}
		b.pendingTabs[id] = pending
		// Timeout is ambiguous: the host may already have started the child.
		// Drain any late acknowledgement, but never execute a duplicate locally.
		b.status = "Tab spawn not confirmed; not launching a duplicate"
		if !b.promptActive && !b.manualActive && !b.mp3Active {
			b.drawStatus()
			b.flush()
		}
	}
	if len(b.tabFallbacks) > 0 && len(b.pendingTabs) == 0 && !b.promptActive && !b.manualActive && !b.mp3Active && b.running {
		pending := b.tabFallbacks[0]
		b.tabFallbacks = b.tabFallbacks[1:]
		path := pending.path
		if pending.transfer != "" {
			if _, err := os.Stat(path); err != nil {
				path = pending.request.Command[1]
			}
		}
		b.runTerminalToolIn(pending.directory, pending.tool, path)
		if pending.transfer != "" && path == pending.path {
			_ = os.RemoveAll(pending.transfer)
		}
	}
}
