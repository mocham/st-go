package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"st-go/aiharness"
	"st-go/term"
)

const (
	slotInitial    uint32 = 0xffaaaaaa
	slotProcessing uint32 = 0xffffd75f
	slotReady      uint32 = 0xff5fd787
	slotInserted   uint32 = 0xffffffff
)

func (e *Editor) inlineSlotStatus(b *Buffer) term.RegionStatus {
	text := previewText(b)
	blocks := aiharness.Blocks(text)
	var status term.RegionStatus
	if len(blocks) == 0 {
		return status
	}
	states := map[string]string{}
	project, err := aiharness.Resolve(b.meta.Path, text)
	if err == nil {
		// Slot creation alone needs neither a server nor a database. Reopen a
		// previously saved dictionary only when it exists.
		if _, err := os.Stat(project.Database); err == nil {
			if h, _, err := e.inlineProject(b); err == nil {
				states, _ = h.States(b.meta.Path)
			}
		}
	}
	seen := make(map[string]bool)
	for _, block := range blocks {
		if seen[block.UUID] {
			continue
		}
		seen[block.UUID] = true
		label, color := "Initial", slotInitial
		if string(b.text[block.Content:block.Close]) != aiharness.Slot {
			label, color = "Inserted", slotInserted
		} else {
			switch states[block.UUID] {
			case "queued", "running":
				label, color = "Processing", slotProcessing
			case "ready", "inserted":
				label, color = "Ready", slotReady
			}
		}
		status.Items = append(status.Items, term.RegionStatusItem{Text: block.UUID[:8] + " " + label, Color: color})
	}
	if len(status.Items) > 256 {
		extra := len(status.Items) - 255
		status.Items = append(status.Items[:255], term.RegionStatusItem{Text: fmt.Sprintf("+%d slots", extra), Color: slotInitial})
	}
	return status
}

func (p *editorPreview) syncSlotStatus(e *Editor, now time.Time) error {
	var b *Buffer
	for _, candidate := range e.buffers {
		if candidate.meta.Path == p.path {
			b = candidate
			break
		}
	}
	if b != nil && p.slotBuffer == b && p.slotRevision == b.syncRevision && now.Sub(p.slotCheck) < 500*time.Millisecond {
		return nil
	}
	p.slotBuffer, p.slotCheck = b, now
	var status term.RegionStatus
	if b != nil {
		p.slotRevision = b.syncRevision
		status = e.inlineSlotStatus(b)
	}
	payload := "clear"
	if len(status.Items) > 0 {
		data, err := json.Marshal(status)
		if err != nil {
			return err
		}
		payload = base64.StdEncoding.EncodeToString(data)
	}
	if payload == p.slotPayload {
		return nil
	}
	// Avoid sending a new extension to terminals until this document has slots.
	if payload == "clear" && p.slotPayload == "" {
		return nil
	}
	if err := p.command("region secondary status " + payload); err != nil {
		return err
	}
	p.slotPayload = payload
	return nil
}
