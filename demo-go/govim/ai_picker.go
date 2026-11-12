package main

import (
	"fmt"
	"sort"
)

// Reuse the editor's transient pager area; the document preview stays in place.
func (e *Editor) aiPick(command string) {
	a := e.ai
	a.picker = nil
	a.pickerIndex = 0
	a.pickerCommand = command
	for _, p := range a.proposals {
		if p.Status != "inserted" && p.Status != "dismissed" {
			a.picker = append(a.picker, p)
		}
	}
	sort.Slice(a.picker, func(i, j int) bool { return a.picker[i].ID < a.picker[j].ID })
	if len(a.picker) == 0 {
		e.message = "No pending AI proposals"
		return
	}
	e.aiDrawPicker()
}
func (e *Editor) aiDrawPicker() {
	a := e.ai
	e.pager = []string{a.pickerCommand + ": choose proposal · j/k or arrows · Enter · Esc"}
	first := max(0, a.pickerIndex-max(1, e.rows-5)/2)
	for i := first; i < min(len(a.picker), first+max(1, e.rows-4)); i++ {
		p := a.picker[i]
		prefix := "  "
		if i == a.pickerIndex {
			prefix = "> "
		}
		e.pager = append(e.pager, fmt.Sprintf("%s%s  %s  %s", prefix, p.ID, p.Status, p.Document))
	}
	e.dirty = true
}
func (e *Editor) aiPickerKey(key string) bool {
	a := e.ai
	if a == nil || len(a.picker) == 0 {
		return false
	}
	switch key {
	case "j", "<Down>":
		a.pickerIndex = min(len(a.picker)-1, a.pickerIndex+1)
	case "k", "<Up>":
		a.pickerIndex = max(0, a.pickerIndex-1)
	case "<CR>", "<Enter>":
		p := a.picker[a.pickerIndex]
		command := a.pickerCommand
		a.picker = nil
		e.pager = nil
		if command == "AIRefine" {
			if err := e.aiSaveDraft(); err != nil {
				e.error(err)
				return true
			}
			e.error(e.aiSubmit(p, a.pickerArgs))
			e.aiPreview(p, true)
			return true
		}
		e.aiCommand(command, p.ID)
		return true
	case "<Esc>", "q":
		a.picker = nil
		e.pager = nil
		e.dirty = true
		return true
	}
	e.aiDrawPicker()
	return true
}
