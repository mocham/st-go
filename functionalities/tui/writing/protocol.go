// Package writing is the versioned govim/opencode-tui writing contract. File
// paths name immutable local snapshots; no OpenCode wire types cross this API.
package writing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"st-go/limits"
	"st-go/tui/appbus"
)

const Version = 1
const MaxResponse = 512 << 10
const ContextRunes = 24000 // roughly 6k tokens for ordinary English; a hard size bound

type Message struct {
	Version     int    `json:"version"`
	Type        string `json:"type"`
	To          string `json:"to,omitempty"`
	RequestID   string `json:"requestID,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Project     string `json:"project,omitempty"`
	Document    string `json:"document,omitempty"`
	Manifest    string `json:"manifest,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	Draft       string `json:"draft,omitempty"`
	Text        string `json:"text,omitempty"`
	Reasoning   string `json:"reasoning,omitempty"`
	Prediction  string `json:"prediction,omitempty"`
	Error       string `json:"error,omitempty"`
	Session     string `json:"session,omitempty"`
	Model       string `json:"model,omitempty"`
}

type File struct {
	Revision uint64 `json:"revision"`
	Path     string `json:"path"`
	Snapshot string `json:"snapshot"`
	Hash     string `json:"hash"`
}
type Manifest struct {
	Version     int    `json:"version"`
	Project     string `json:"project"`
	Document    string `json:"document"`
	Placeholder string `json:"placeholder"`
	CursorRune  int    `json:"cursorRune"`
	Files       []File `json:"files"`
}

func Hash(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func Root(path string) string {
	path, _ = filepath.Abs(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		path = filepath.Dir(path)
	}
	initial := path
	for {
		for _, name := range []string{".govim", ".git", "go.mod"} {
			if _, err := os.Stat(filepath.Join(path, name)); err == nil {
				return path
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return initial
		}
		path = parent
	}
}
func Channel(root string) string { return "govim-ai:" + Hash(filepath.Clean(root))[:24] }
func Marker(path, id string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".markdown" || ext == ".mdown" || ext == ".mkd" {
		return "<!-- govim-ai:" + id + " -->"
	}
	return "% govim-ai:" + id
}
func UUID() string {
	bytes, _ := hex.DecodeString(appbus.ID())
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	id := hex.EncodeToString(bytes)
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
func ValidUUID(id string) bool {
	return len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-' && appbus.ValidID(strings.ReplaceAll(id, "-", ""))
}
func Markers(request string) (string, string) {
	return "<<<GOVIM_PREDICTION_" + request + "_BEGIN>>>", "<<<GOVIM_PREDICTION_" + request + "_END>>>"
}
func Prediction(text, request string) (string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	begin, end := Markers(request)
	if strings.Count(text, begin) != 1 || strings.Count(text, end) != 1 {
		return "", fmt.Errorf("response needs exactly one matching prediction block")
	}
	a, b := strings.Index(text, begin), strings.Index(text, end)
	if b < a+len(begin) {
		return "", fmt.Errorf("prediction delimiters out of order")
	}
	if a > 0 && text[a-1] != '\n' || a+len(begin) >= len(text) || text[a+len(begin)] != '\n' || b == 0 || text[b-1] != '\n' || b+len(end) < len(text) && text[b+len(end)] != '\n' {
		return "", fmt.Errorf("prediction markers must occupy separate lines")
	}
	result := text[a+len(begin)+1 : b]
	result = strings.TrimSuffix(result, "\n")
	if strings.TrimSpace(result) == "" || len(result) > MaxResponse || !utf8.ValidString(result) {
		return "", fmt.Errorf("empty or oversized prediction")
	}
	return result, nil
}

func Within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// ReadContext resolves only client-local snapshot paths. Each object is hashed;
// edits after submission cannot alter the context consumed by a request.
func ReadContext(request Message, project string) (string, error) {
	if request.Version != Version || !appbus.ValidID(request.RequestID) || !ValidUUID(request.Placeholder) || filepath.Clean(request.Project) != filepath.Clean(project) {
		return "", fmt.Errorf("invalid writing request/project")
	}
	storage := filepath.Join(project, ".govim", "ai-snapshots")
	if !Within(storage, request.Manifest) {
		return "", fmt.Errorf("snapshot manifest is outside the project store")
	}
	data, err := limits.ReadFile(request.Manifest)
	if err != nil {
		return "", err
	}
	if len(data) > 256<<10 {
		return "", fmt.Errorf("manifest too large")
	}
	var manifest Manifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	if manifest.Version != Version || manifest.Project != project || manifest.Document != request.Document || manifest.Placeholder != request.Placeholder || len(manifest.Files) > 128 {
		return "", fmt.Errorf("snapshot identity mismatch")
	}
	var main string
	var extras strings.Builder
	bytes := 0
	for _, file := range manifest.Files {
		if !Within(storage, file.Snapshot) {
			return "", fmt.Errorf("invalid snapshot path")
		}
		data, err := limits.ReadFile(file.Snapshot)
		if err != nil {
			return "", err
		}
		text := string(data)
		bytes += len(data)
		if bytes > limits.FileBytes {
			return "", fmt.Errorf("snapshot set too large")
		}
		if Hash(text) != file.Hash {
			return "", fmt.Errorf("snapshot hash mismatch for %s", file.Path)
		}
		if file.Path == request.Document {
			main = text
		} else if extras.Len() < 6000 {
			for _, line := range strings.Split(text, "\n") {
				s := strings.TrimSpace(line)
				if strings.HasPrefix(s, "\\newcommand") || strings.HasPrefix(s, "\\def") || strings.HasPrefix(s, "\\section") || strings.HasPrefix(s, "#") {
					if extras.Len()+len(line) < 6000 {
						extras.WriteString(line + "\n")
					}
				}
			}
		}
	}
	marker := Marker(request.Document, request.Placeholder)
	if strings.Count(main, marker) != 1 {
		return "", fmt.Errorf("snapshot must contain exactly one placeholder")
	}
	before, after, _ := strings.Cut(main, marker)
	left, right := []rune(before), []rune(after)
	// Prioritize nearby prose; preserve an outline and declarations from earlier
	// source rather than spending the entire model budget on a long document.
	if len(left)+len(right) > ContextRunes-2000 {
		left = left[max(0, len(left)-16000):]
		right = right[:min(len(right), 4000)]
	}
	var outline strings.Builder
	for _, line := range strings.Split(main, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "\\section") || strings.HasPrefix(s, "\\subsection") || strings.HasPrefix(s, "\\newcommand") {
			if outline.Len()+len(line) < 4000 {
				outline.WriteString(line + "\n")
			}
		}
	}
	definitions := []rune(outline.String() + extras.String())
	definitions = definitions[:min(len(definitions), max(0, ContextRunes-500-len(left)-len(right)))]
	context := fmt.Sprintf("Document: %s\nOutline/definitions:\n%s\nBEFORE INSERTION:\n%s\n<<< INSERT NEXT PARAGRAPH HERE >>>\nAFTER INSERTION:\n%s", request.Document, string(definitions), string(left), string(right))
	begin, end := Markers(request.RequestID)
	return fmt.Sprintf("Predict the next paragraph for placeholder %s. The following immutable snapshot is the authoritative current writing. Match its language, voice and notation. Produce document source, preserving Markdown or LaTeX as appropriate. This is a writing suggestion; return text for the author to insert.\nInstruction: %s\nCurrent author-edited draft (when refining):\n%s\n\n%s\n\nEnclose the final prediction exactly once between these markers, each on its own line:\n%s\nparagraph source\n%s\nAny brief editorial explanation belongs outside that block. Return the new paragraph only inside the block; do not repeat surrounding source.", request.Placeholder, request.Instruction, request.Draft, context, begin, end), nil
}
