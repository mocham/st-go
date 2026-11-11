package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
)

// Isolate PATH so installed viewers never influence default/fallback tests.
// Include only the standard utilities used by the Bash browser and fixtures.
func documentToolTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"bash", "stty", "mktemp", "rm", "file", "stat", "ln", "date", "cat", "head", "grep"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeDocumentTool(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
}

func documentBrowserCommands(t *testing.T) map[string][]string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := filepath.Abs("../../demo/file-browser.sh")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]string{"go": {browserBin}, "bash": {bash, shell}}
}

const interactiveDocumentTool = `
[[ -t 0 && -t 1 && -t 2 ]] || exit 81
printf '%s|%s|%s|%s' "${0##*/}" "$#" "$1" "$PWD" > "$MARKER"
printf '\033[?1049h\033Pregion split 50%%\033\\DOCUMENT_TOOL_READY\n'
# Deliberately leave raw termios and region state behind on exit, including a
# failed exit: the browser must still recover its own UI and input modes.
stty -echo -icanon min 1 time 0
IFS= read -rsn1 key
[[ $key == q ]] || exit 82
printf 'created' > "$CREATED"
exit "${TOOL_EXIT:-0}"
`

func TestDocumentToolsUseSameTerminalAndReturn(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		t.Run(implementation, func(t *testing.T) {
			tests := []struct{ name, tool, activate, result string }{
				{"a ';$literal.pdf", "zat", "\r", "zat finished"},
				{"a ';$literal.DJVU", "zat-djvu", "\r", "zat-djvu finished"},
				{"a document.tex", "govim", "o", "govim finished"},
				{"a document.md", "govim", "double", "govim finished"},
				{"a document.PDF", "zat", "\r", "zat finished"},
				{"a document.TeX", "govim", "\r", "govim finished"},
				{"a document.MD", "govim", "\r", "govim"},
				{"a script.sh", "govim", "o", "govim finished"},
				{"a note.txt", "govim", "\r", "govim finished"},
				{"a preset.vim", "govim", "\r", "govim finished"},
				{"a preset.yaml", "govim", "\r", "govim finished"},
				{"a preset.cc", "govim", "\r", "govim finished"},
				{"a textual.pem", "govim", "\r", "govim finished"},
				{"Dockerfile", "govim", "\r", "govim finished"},
				{".bashrc", "govim", "\r", "govim finished"},
				{"plain.extensionless-custom", "govim", "\r", "govim finished"},
				{"path document.pdf", "zat", "path", "zat finished"},
			}
			if implementation == "go" {
				tests = append(tests,
					struct{ name, tool, activate, result string }{"a image.png", "goiv", "\r", "goiv finished"},
					struct{ name, tool, activate, result string }{"a image.WEBP", "goiv", "o", "goiv finished"},
				)
			}
			for _, test := range tests {
				t.Run(filepath.Ext(test.name), func(t *testing.T) {
					dir, tools := t.TempDir(), documentToolTestPath(t)
					writeFiles(t, dir, test.name)
					if filepath.Ext(test.name) == ".pem" {
						if err := os.WriteFile(filepath.Join(dir, test.name), []byte("-----BEGIN CERTIFICATE-----\nU3ludGhldGljIHRlc3Qgb25seQ==\n-----END CERTIFICATE-----\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					writeDocumentTool(t, tools, test.tool, interactiveDocumentTool)
					marker, created := filepath.Join(tools, "opened"), filepath.Join(dir, "0-created.txt")
					env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "CREATED="+created, "ST_FILE_BROWSER_HIDDEN=1"), "ST_FILE_BROWSER_OPEN")
					if filepath.Ext(test.name) == ".MD" {
						env = append(env, "TOOL_EXIT=7")
					}
					f := startBrowserCommand(t, env, append(append([]string{}, program...), dir)...)
					f.send("\x1b[B")
					before := len(f.output())
					if test.activate == "double" {
						f.send("\x1b[<0;60;9M\x1b[<0;60;9M")
					} else if test.activate == "path" {
						f.send("/" + strings.TrimPrefix(filepath.Join(dir, test.name), "/") + "\r")
					} else {
						f.send(test.activate)
					}
					waitFileContent(t, marker, test.tool+"|1|"+filepath.Join(dir, test.name)+"|"+dir)
					waitContains(t, f, "DOCUMENT_TOOL_READY", "child owns the terminal")
					if strings.Contains(f.output()[before:], "place bottom-left") {
						t.Fatal("in-terminal opener collapsed the window")
					}
					if strings.Contains(f.output()[before:], test.result) {
						t.Fatal("browser resumed while child was still running")
					}
					if err := ptyutil.SetWinSize(f.master, 26, 90); err != nil {
						t.Fatal(err)
					}
					// q belongs to the tool, not the browser's input loop.
					f.send("q")
					waitFor(t, f, func() bool {
						out := f.output()[before:]
						return strings.Contains(out, "region reset") && strings.Contains(out, "0-created.txt") && strings.Contains(out, test.result)
					}, "browser mode/preview/list restoration")
					f.send("r")
					waitContains(t, f, "Refreshed", "browser input restored after viewer")
					// Opening again must still target the original selection, even
					// though the tool created an earlier-sorting directory entry.
					if err := os.Remove(marker); err != nil {
						t.Fatal(err)
					}
					f.send("o")
					waitFileContent(t, marker, test.tool+"|1|"+filepath.Join(dir, test.name)+"|"+dir)
					f.send("q")
					f.send("q")
					waitContains(t, f, "window forget file-browser-", "browser exits normally after tool")
					mode, err := unix.IoctlGetTermios(int(f.master.Fd()), unix.TCGETS)
					if err != nil || mode.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != (unix.ICANON|unix.ECHO|unix.ISIG) {
						t.Fatalf("original termios not restored: %+v, %v", mode, err)
					}
				})
			}
		})
	}
}

func TestDocumentToolDispatchAndFallbacks(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		t.Run(implementation, func(t *testing.T) {
			for _, mode := range []string{"missing-pdf", "missing-djvu", "missing-image", "missing-tex", "missing-md", "missing-txt", "missing-sh", "custom-flag", "custom-env", "disabled"} {
				t.Run(mode, func(t *testing.T) {
					dir, tools := t.TempDir(), documentToolTestPath(t)
					name := "document.pdf"
					if mode == "missing-tex" {
						name = "document.tex"
					}
					if mode == "missing-djvu" {
						name = "document.djvu"
					}
					if mode == "missing-md" {
						name = "document.md"
					}
					if mode == "missing-txt" {
						name = "document.txt"
					}
					if mode == "missing-sh" {
						name = "document.sh"
					}
					if mode == "missing-image" {
						name = "image.png"
					}
					writeFiles(t, dir, name)
					marker := filepath.Join(tools, "opened")
					writeDocumentTool(t, tools, "st", `printf '%s|%s' "$1" "$2" > "$MARKER"`)
					writeDocumentTool(t, tools, "bar", `printf '%s|%s' "$1" "$2" > "$MARKER"`)
					writeDocumentTool(t, tools, "xdg-open", `printf '%s' "$1" > "$MARKER"`)
					// Legacy vimtex must no longer win default dispatch, even when
					// govim is missing. Keep the established external fallback instead.
					writeDocumentTool(t, tools, "vimtex", `printf 'wrong legacy tool' > "$MARKER"`)
					if !strings.HasPrefix(mode, "missing-") {
						for _, tool := range []string{"zat", "zat-djvu", "goiv", "govim"} {
							writeDocumentTool(t, tools, tool, `printf 'wrong tool' > "$MARKER"`)
						}
					} else {
						tool, other := "zat", "govim"
						switch mode {
						case "missing-djvu":
							tool, other = "zat-djvu", "zat"
						case "missing-image":
							tool, other = "goiv", "zat"
						case "missing-tex", "missing-md", "missing-txt", "missing-sh":
							tool, other = "govim", "zat"
						}
						writeDocumentTool(t, tools, other, `printf 'wrong tool' > "$MARKER"`)
						// An existing but non-executable file is not an available tool.
						if err := os.WriteFile(filepath.Join(tools, tool), []byte("not executable"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_EXECUTABLE="+filepath.Join(tools, "st")), "ST_FILE_BROWSER_OPEN")
					args := append([]string{}, program...)
					code := `printf '%s|%s|%s' "$FILE" "$NAME" "$BROWSER_DIR" > "$MARKER"`
					switch mode {
					case "custom-flag":
						args = append(args, "--open", code)
					case "custom-env":
						env = append(env, "ST_FILE_BROWSER_OPEN="+code)
					case "disabled":
						args = append(args, "--open=")
					}
					f := startBrowserCommand(t, env, append(args, dir)...)
					f.send("\x1b[Bo")
					if mode == "disabled" {
						waitContains(t, f, "External open", "disabled opener")
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatal("disabled opening launched a tool")
						}
						return
					}
					file := filepath.Join(dir, name)
					want := file
					if implementation == "go" && (mode == "missing-pdf" || mode == "missing-djvu" || mode == "missing-image") {
						want = "external|" + file
					}
					if mode == "missing-tex" || mode == "missing-md" || mode == "missing-txt" || mode == "missing-sh" {
						want = "vim|" + file
					}
					if strings.HasPrefix(mode, "custom-") {
						want = file + "|" + name + "|" + dir
					}
					waitFileContent(t, marker, want)
				})
			}
		})
	}
}

func TestEditorTextClassification(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name, content string
		want          bool
	}{
		{"script.sh", "#!/bin/sh\necho synthetic\n", true},
		{"notes.txt", "Synthetic note\n", true}, {"preset.vim", "set number\n", true},
		{"Dockerfile", "FROM scratch\n", true}, {".bash_profile", "export SYNTHETIC=1\n", true},
		{"unknown-format", "ordinary synthetic text\n", true}, {"empty.yaml", "", true}, {"empty.unknown", "", true},
		{"textual.pem", "-----BEGIN CERTIFICATE-----\nU3ludGhldGljIHRlc3Qgb25seQ==\n-----END CERTIFICATE-----\n", true},
		{"image.png", "", false}, {"scan.djvu", "", false}, {"video.mp4", "", false}, {"archive.zip", "", false}, {"database.sqlite3", "", false},
		{"office.docx", "", false}, {"program.wasm", "", false}, {"binary.unknown", "\x00\x01\x02\x03", false},
	} {
		path := filepath.Join(dir, test.name)
		if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := editorTextFile(path); got != test.want {
			t.Errorf("%s text=%v want %v", test.name, got, test.want)
		}
	}
	fifo := filepath.Join(dir, "pipe.sh")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if editorTextFile(fifo) {
		t.Fatal("non-regular file treated as editable text")
	}
}

func TestDocumentToolInterruptReturnsToBrowser(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		t.Run(implementation, func(t *testing.T) {
			dir, tools := t.TempDir(), documentToolTestPath(t)
			writeFiles(t, dir, "document.pdf")
			writeDocumentTool(t, tools, "zat", "printf 'INTERRUPT_READY\\n'\nIFS= read -r line\n")
			env := envWithout(testEnv("PATH="+tools), "ST_FILE_BROWSER_OPEN")
			f := startBrowserCommand(t, env, append(append([]string{}, program...), dir)...)
			f.send("\x1b[Bo")
			waitContains(t, f, "INTERRUPT_READY", "interruptible viewer")
			// The controlling terminal sends this to the foreground group.
			f.send("\x03")
			waitContains(t, f, "region reset", "return after interrupted viewer")
			f.send("r")
			waitContains(t, f, "Refreshed", "Ctrl+C did not quit the browser")
			if err := f.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
		})
	}
}
