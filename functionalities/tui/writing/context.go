package writing

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ContextRange is a half-open, zero-based rune range in the document snapshot.
// It is recorded at submission, so subsequent edits cannot move its boundaries.
type ContextRange struct {
	StartRune int `json:"startRune"`
	EndRune   int `json:"endRune"`
}

func Checkpoint(path string) string { return Marker(path, "checkpoint") }

// CheckpointRange selects only source after the last standalone checkpoint
// comment and before the cursor. Without a checkpoint it starts at rune zero.
func CheckpointRange(path, text string, cursor int) (ContextRange, error) {
	span := ContextRange{EndRune: cursor}
	if !utf8.ValidString(text) || cursor < 0 || cursor > utf8.RuneCountInString(text) {
		return span, fmt.Errorf("invalid context cursor")
	}
	marker := Checkpoint(path)
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if offset >= cursor {
			break
		}
		next := offset + utf8.RuneCountInString(line)
		if strings.TrimSpace(line) == marker {
			if next > cursor {
				// A cursor inside the comment must not send a partial marker.
				span.EndRune = offset
				break
			}
			span.StartRune = next
		}
		offset = next
	}
	if span.EndRune-span.StartRune > ContextRunes {
		return span, fmt.Errorf("AI context exceeds %d characters; add a closer :AICheckpoint", ContextRunes)
	}
	return span, nil
}
