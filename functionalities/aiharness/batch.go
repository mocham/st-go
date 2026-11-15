package aiharness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const MaxBatchSlots = 256
const MaxBatchResponse = 8 << 20

// BatchRequest is one model call with a shared, immutable input snapshot.
// UUIDs are listed in document order; each receives its own dictionary version.
type BatchRequest struct {
	UUIDs           []string
	Document        string
	Instruction     string
	Context         string
	CurrentDocument string
	Model           *Model
}

func singleBatch(r Request) BatchRequest {
	return BatchRequest{[]string{r.UUID}, r.Document, r.Instruction, r.Context, r.CurrentDocument, r.Model}
}

func Prompt(r Request) string { return BatchPrompt(singleBatch(r)) }

func BatchPrompt(r BatchRequest) string {
	ids, _ := json.Marshal(r.UUIDs)
	return "Instruction:\n" + r.Instruction +
		"\n\nTarget placeholder UUIDs (document order):\n" + string(ids) +
		"\n\nResponse format:\nReturn exactly one JSON object mapping each requested UUID to its insertion text. " +
		"Include every requested UUID exactly once and no other keys. Each value must be a nonempty string containing only that slot's Markdown or LaTeX paragraph source. " +
		"Plan the insertions together so they form a coherent document. Fill only the listed UUIDs, even if the document contains other placeholders. " +
		"Use valid JSON escaping: double LaTeX backslashes and encode line breaks as \\n. Do not include Markdown fences, commentary, placeholder markers, or document wrappers.\n\nContext:\n" +
		r.Context + "\n\nCurrent document:\n" + r.CurrentDocument
}

func validateBatch(r BatchRequest) error {
	if len(r.UUIDs) == 0 || len(r.UUIDs) > MaxBatchSlots {
		return fmt.Errorf("AI batch requires 1 to %d slots", MaxBatchSlots)
	}
	if r.Document == "" || !utf8.ValidString(r.CurrentDocument+r.Context+r.Instruction) {
		return fmt.Errorf("invalid AI request")
	}
	if len(r.CurrentDocument)+len(r.Context)+len(r.Instruction) > MaxInput {
		return fmt.Errorf("AI instruction, context and document exceed %d bytes", MaxInput)
	}
	blocks := make(map[string]Block)
	counts := make(map[string]int)
	for _, b := range Blocks(r.CurrentDocument) {
		blocks[b.UUID] = b
		counts[b.UUID]++
	}
	runes := []rune(r.CurrentDocument)
	seen := make(map[string]bool)
	for _, id := range r.UUIDs {
		if !ValidUUID(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate AI target UUID")
		}
		seen[id] = true
		if counts[id] != 1 || strings.Count(r.CurrentDocument, "{{{%"+id+"\n") != 1 {
			return fmt.Errorf("AI block missing, malformed, or duplicated")
		}
		block := blocks[id]
		if string(runes[block.Content:block.Close]) != Slot {
			return fmt.Errorf("AI target must contain an empty %s slot", Slot)
		}
	}
	return nil
}

func validParagraph(id, text string) bool {
	block, err := FindBlock(BlockText(id, text), id)
	return text != "" && len(text) <= MaxParagraph && utf8.ValidString(text) &&
		!strings.Contains(text, Slot) && !strings.Contains(text, "{{{%") &&
		err == nil && block.End == utf8.RuneCountInString(BlockText(id, text))
}

// ParseInsertions validates the entire reply before any slot becomes Ready.
// Decode keys individually: json.Unmarshal into a map would hide duplicate UUIDs.
func ParseInsertions(text string, ids []string) (map[string]string, error) {
	if len(text) > MaxBatchResponse || !utf8.ValidString(text) {
		return nil, fmt.Errorf("AI batch response must be UTF-8 and at most %d bytes", MaxBatchResponse)
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("AI response must be a JSON object mapping UUIDs to insertion strings")
	}
	paragraphs := make(map[string]string, len(ids))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid AI JSON object")
		}
		id, ok := key.(string)
		if !ok || !wanted[id] {
			return nil, fmt.Errorf("AI response contains an unrequested UUID")
		}
		if _, duplicate := paragraphs[id]; duplicate {
			return nil, fmt.Errorf("AI response contains a duplicate UUID")
		}
		var paragraph string
		if err := decoder.Decode(&paragraph); err != nil {
			return nil, fmt.Errorf("AI insertion must be a JSON string")
		}
		paragraph = strings.TrimSpace(paragraph)
		if !validParagraph(id, paragraph) {
			return nil, fmt.Errorf("invalid AI insertion for %s: empty, oversized, or contains unmatched/reserved block markers", id)
		}
		paragraphs[id] = paragraph
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("incomplete AI JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("unexpected text after AI JSON object")
	}
	if len(paragraphs) != len(wanted) {
		return nil, fmt.Errorf("AI response is missing %d requested UUIDs", len(wanted)-len(paragraphs))
	}
	return paragraphs, nil
}
