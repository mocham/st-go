package writing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestSnapshotContextUsesUnsavedHashAndPreservesFollowingText(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".govim", "ai-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := UUID()
	text := strings.Repeat("Earlier text. ", 3000) + "\n" + Marker("paper.md", id) + "\nFollowing material."
	object := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(object, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: Version, Project: root, Document: "paper.md", Placeholder: id, Files: []File{{Path: "paper.md", Snapshot: object, Hash: Hash(text)}}}
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
	if !strings.Contains(context, "Following material.") || len([]rune(context)) > ContextRunes+2000 {
		t.Fatal("context budget lost following text")
	}
	if err := os.WriteFile(object, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(request, root); err == nil {
		t.Fatal("accepted mutated snapshot")
	}
}
