package writing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"st-go/tui/appbus"
)

func TestPredictionRejectsAmbiguousBlocks(t *testing.T) {
	id := appbus.ID()
	begin, end := Markers(id)
	valid := "An explanation.\n" + begin + "\nThe predicted paragraph.\n" + end + "\nCommentary."
	got, err := Prediction(valid, id)
	if err != nil || got != "The predicted paragraph." {
		t.Fatalf("%q %v", got, err)
	}
	for _, text := range []string{begin + "\npartial", valid + "\n" + valid, "prefix " + begin + "\nparagraph\n" + end, end + "\n" + begin, begin + "\n\n" + end} {
		if _, err := Prediction(text, id); err == nil {
			t.Fatalf("accepted malformed response %q", text)
		}
	}
}

func TestSnapshotContextUsesOnlyFrozenCheckpointToCursorRange(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".govim", "ai-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := UUID()
	before := strings.Repeat("Earlier text. ", 3000) + "\n# Earlier heading\n" + Checkpoint("paper.md") + "\n"
	excerpt := "最新段落 αβ.\nCurrent text "
	text := before + excerpt + "after the cursor.\n" + Marker("paper.md", id) + "\n# Following material."
	object := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(object, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cursor := utf8.RuneCountInString(before + excerpt)
	span, err := CheckpointRange("paper.md", text, cursor)
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dir, "other.txt")
	extraText := "# Other buffer heading\n\\newcommand{\\secret}{Other definition}"
	if err := os.WriteFile(extra, []byte(extraText), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: Version, Project: root, Document: "paper.md", Placeholder: id, CursorRune: cursor, Context: &span, Files: []File{{Path: "paper.md", Snapshot: object, Hash: Hash(text)}, {Path: "other.tex", Snapshot: extra, Hash: Hash(extraText)}}}
	data, _ := json.Marshal(manifest)
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	request := Message{Version: Version, Project: root, Document: "paper.md", Manifest: path, Placeholder: id, RequestID: appbus.ID()}
	context, err := ReadContext(request, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(context, excerpt) || len([]rune(context)) > ContextRunes+2000 {
		t.Fatal("selected source excerpt missing or oversized")
	}
	for _, excluded := range []string{"Earlier text", "Earlier heading", "after the cursor", "Following material", "Other buffer", "Other definition", Checkpoint("paper.md")} {
		if strings.Contains(context, excluded) {
			t.Fatalf("prompt included text outside selected context: %s", excluded)
		}
	}
	// The recorded range is checked against the immutable source, not trusted as
	// an arbitrary selection that could reintroduce old or following material.
	manifest.Context.StartRune--
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(request, root); err == nil {
		t.Fatal("accepted mismatched context bounds")
	}
	manifest.Context.StartRune++
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(request, root); err == nil {
		t.Fatal("accepted mutated snapshot")
	}
}

func TestCheckpointRangeBoundaries(t *testing.T) {
	for _, path := range []string{"paper.md", "paper.tex"} {
		t.Run(path, func(t *testing.T) {
			marker := Checkpoint(path)
			cases := []struct {
				name, before, after, want string
			}{
				{"beginning", "前文\n", "Following text", "前文\n"},
				{"latest", marker + "\nOld.\n" + marker + "\n新文", " tail\n" + marker + "\nLater", "新文"},
				{"inline-is-not-marker", "Text " + marker + "\nCurrent", "", "Text " + marker + "\nCurrent"},
				{"crlf", "Old\r\n  " + marker + "\r\nCurrent\r\n", "Later", "Current\r\n"},
				{"at-marker-start", "Old\n", marker + "\nNew", "Old\n"},
				{"inside-marker", "Old\n" + marker[:4], marker[4:] + "\nNew", "Old\n"},
				{"empty", marker + "\n", "New", ""},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					text := tc.before + tc.after
					span, err := CheckpointRange(path, text, utf8.RuneCountInString(tc.before))
					if err != nil {
						t.Fatal(err)
					}
					if got := string([]rune(text)[span.StartRune:span.EndRune]); got != tc.want {
						t.Fatalf("excerpt %q, want %q", got, tc.want)
					}
				})
			}
		})
	}
	for _, cursor := range []int{-1, 4} {
		if _, err := CheckpointRange("paper.md", "你好", cursor); err == nil {
			t.Fatal("accepted invalid rune offset")
		}
	}
	long := strings.Repeat("文", ContextRunes+1)
	if _, err := CheckpointRange("paper.md", long, ContextRunes+1); err == nil || !strings.Contains(err.Error(), ":AICheckpoint") {
		t.Fatal("oversized range was silently included or truncated")
	}
}
