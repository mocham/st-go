package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func completionPrompt(dir, input string) *Browser {
	b := &Browser{dir: dir, promptMode: "path", promptActive: true, promptBuffer: []byte(input), pathMatchIdx: -1}
	b.refreshPromptMatches()
	return b
}

func TestPathTabFillsFirstMatchWithoutSubmitting(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "alpha-one.txt", "alpha-two.txt", "café 世界.txt")
	for _, test := range []struct{ input, want string }{
		{"al", "alpha-one.txt"},
		{"alpha-*", "alpha-one.txt"},
		{filepath.Join(dir, "al"), filepath.Join(dir, "alpha-one.txt")},
		{"caf", "café 世界.txt"},
	} {
		t.Run(test.input, func(t *testing.T) {
			b := completionPrompt(dir, test.input)
			b.pathSelectDelta(1)
			b.pathSelectDelta(1) // Tab always takes the first, not the highlighted match.
			b.handlePromptKey('\t')
			if string(b.promptBuffer) != test.want {
				t.Fatalf("completion=%q want %q", b.promptBuffer, test.want)
			}
			if !b.promptActive || b.promptAbort || b.dir != dir || b.promptMode != "path" {
				t.Fatal("completion left input mode or navigated")
			}
			if b.promptCursor != len(test.want) || b.promptEditor.Cursor != utf8.RuneCountInString(test.want) {
				t.Fatal("completion did not put the cursor at the end")
			}
		})
	}
}

func TestPathTabDirectoriesAndLiteralTargets(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "alpha[dir]")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, nested, "first[one].txt", "second.txt")
	b := completionPrompt(dir, "al")
	b.handlePromptKey('\t')
	if string(b.promptBuffer) != "alpha[dir]/" || b.pathCompleted != nested || !b.promptActive {
		t.Fatalf("directory completion=%q target=%q", b.promptBuffer, b.pathCompleted)
	}
	b.handlePromptKey('\t')
	want := filepath.Join(nested, "first[one].txt")
	if b.pathCompleted != want || string(b.promptBuffer) != "alpha[dir]/first[one].txt" || !b.promptActive || b.dir != dir {
		t.Fatalf("nested completion=%q target=%q", b.promptBuffer, b.pathCompleted)
	}
	// A completed file stays stable; editing invalidates its literal target.
	b.handlePromptKey('\t')
	if b.pathCompleted != want {
		t.Fatal("repeated Tab reinterpreted a literal filename as a glob")
	}
	b.promptEdit("<Home>")
	b.promptDelete()
	if b.pathCompleted != "" {
		t.Fatal("editing retained a stale completed target")
	}
}

func TestPathTabNoMatchLeavesInputUntouched(t *testing.T) {
	b := completionPrompt(t.TempDir(), "does-not-exist")
	b.handlePromptKey('\t')
	if string(b.promptBuffer) != "does-not-exist" || !b.promptActive || b.pathMessage != "No match" || b.pathCompleted != "" {
		t.Fatalf("no-match completion changed input/mode: %+v", b)
	}
}

func TestPathPromptTabPTYStaysInInputModeUntilEnter(t *testing.T) {
	for implementation, command := range documentBrowserCommands(t) {
		for _, kind := range []string{"file", "directory"} {
			t.Run(implementation+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				completed, want, more := "alpha-one.txt", "alpha-one.txt", "\x7f\t"
				if kind == "directory" {
					if err := os.Mkdir(filepath.Join(dir, "alpha-dir"), 0700); err != nil {
						t.Fatal(err)
					}
					completed, want, more = "alpha-dir/", "alpha-dir/first-note.txt", "fir\t"
				}
				writeFiles(t, dir, want, "alpine-two.txt")
				marker := filepath.Join(dir, ".opened")
				args := append(append([]string{}, command...), "--open", `printf '%s' "$FILE" > "$MARKER"`, dir)
				f := startBrowserCommand(t, testEnv("MARKER="+marker), args...)
				f.send("/\x7fal\t")
				waitContains(t, f, " / "+completed, "Tab-filled location input")
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("Tab opened the file before Enter")
				}
				// More typing still edits the prompt; directory completion does
				// not navigate, and file completion does not open the file.
				f.send(more)
				waitContains(t, f, " / "+want, "edited location input after Tab")
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("editing the completed input opened the file before Enter")
				}
				f.send("\r")
				waitFileContent(t, marker, filepath.Join(dir, want))
			})
		}
	}
}

func TestPathPromptTabPTYDirectoriesWildcardsAndLiteralNames(t *testing.T) {
	for implementation, command := range documentBrowserCommands(t) {
		for _, kind := range []string{"absolute", "wildcard", "directory", "literal-directory", "literal-file"} {
			t.Run(implementation+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				input, want := "al\t", "alpha-one.txt"
				switch kind {
				case "absolute":
					input = strings.TrimPrefix(filepath.Join(dir, "al"), "/") + "\t"
				case "wildcard":
					input = "\x7falpha-*\t"
				case "directory", "literal-directory":
					name := "alpha-dir"
					if kind == "literal-directory" {
						name = "alpha[dir]"
					}
					if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
						t.Fatal(err)
					}
					want = name + "/first note.txt"
					input = "\x7fal\t\t"
				case "literal-file":
					want = "alpha[one].txt"
					input = "\x7fal*\t"
				}
				if kind == "absolute" || kind == "wildcard" {
					writeFiles(t, dir, "alpha-one.txt", "alpha-two.txt")
				} else {
					writeFiles(t, dir, want, "alpine-z.txt")
				}
				marker := filepath.Join(dir, ".opened")
				args := append(append([]string{}, command...), "--open", `printf '%s' "$FILE" > "$MARKER"`, dir)
				f := startBrowserCommand(t, testEnv("MARKER="+marker), args...)
				f.send("/" + input + "\r")
				waitFileContent(t, marker, filepath.Join(dir, want))
			})
		}
	}
}
