package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformOpenerSelection(t *testing.T) {
	for _, backend := range []string{"x11", "fb", "web", "unknown", ""} {
		for _, mode := range []string{"default", "legacy-env", "foreign-env", "scoped-env", "all-env", "scoped-empty", "legacy-flag", "scoped-flags", "all-flag", "last-matching"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				for _, name := range []string{"ST_FILE_BROWSER_OPEN", "ST_FILE_BROWSER_OPEN_X11", "ST_FILE_BROWSER_OPEN_FB", "ST_FILE_BROWSER_OPEN_WEB", "ST_FILE_BROWSER_OPEN_ALL"} {
					t.Setenv(name, "")
					if err := os.Unsetenv(name); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("ST_GO_BACKEND", backend)
				active := backend
				if active == "" {
					active = "unknown"
				}
				want, custom := defaultOpenCmd, false
				var args []string
				switch mode {
				case "legacy-env":
					t.Setenv("ST_FILE_BROWSER_OPEN", "legacy")
					if active == "x11" {
						want, custom = "legacy", true
					}
				case "foreign-env":
					foreign := "FB"
					if active == "fb" {
						foreign = "WEB"
					}
					t.Setenv("ST_FILE_BROWSER_OPEN_"+foreign, "foreign")
				case "scoped-env", "scoped-empty":
					t.Setenv("ST_FILE_BROWSER_OPEN_ALL", "all")
					want, custom = "all", true
					for _, platform := range []string{"x11", "fb", "web"} {
						value := platform
						if mode == "scoped-empty" {
							value = ""
						}
						t.Setenv("ST_FILE_BROWSER_OPEN_"+strings.ToUpper(platform), value)
						if active == platform {
							want = value
						}
					}
				case "all-env":
					t.Setenv("ST_FILE_BROWSER_OPEN", "legacy")
					t.Setenv("ST_FILE_BROWSER_OPEN_ALL", "all")
					want, custom = "all", true
				case "legacy-flag":
					args = []string{"--open", "legacy"}
					if active == "x11" {
						want, custom = "legacy", true
					}
				case "scoped-flags":
					args = []string{"--open-for", "x11", "x11", "--open-for=fb", "fb", "--open-for", "web", "web"}
					if active != "unknown" {
						want, custom = active, true
					}
				case "all-flag":
					args = []string{"--open-for", "all", "all"}
					want, custom = "all", true
				case "last-matching":
					t.Setenv("ST_FILE_BROWSER_OPEN_ALL", "environment")
					args = []string{"--open-for", "all", "last", "--open", "legacy"}
					want, custom = "last", true
					if active == "x11" {
						want = "legacy"
					}
				}
				_, command, gotCustom, _, _, done, code := parseArgs(args)
				if done || code != 0 || command != want || gotCustom != custom {
					t.Fatalf("selection=(%q,%t,%t,%d), want (%q,%t,false,0)", command, gotCustom, done, code, want, custom)
				}
			})
		}
	}
	for _, args := range [][]string{{"--open-for"}, {"--open-for", "fb"}, {"--open-for=fb"}, {"--open-for", "typo", "code"}, {"--open-for=", "code"}} {
		_, _, _, _, _, done, code := parseArgs(args)
		if !done || code != 2 {
			t.Fatal("invalid registration accepted", args)
		}
	}
}

func TestBackendAwareOpenersPTY(t *testing.T) {
	for implementation, program := range documentBrowserCommands(t) {
		for _, backend := range []string{"x11", "fb", "web", "unknown"} {
			for _, mode := range []string{"native", "native-text", "legacy-env", "legacy-flag", "scoped-env", "scoped-flag", "all", "disabled", "unhandled", "missing-tool"} {
				t.Run(implementation+"/"+backend+"/"+mode, func(t *testing.T) {
					dir, tools := t.TempDir(), documentToolTestPath(t)
					name := "document '$;.pdf"
					if mode == "unhandled" {
						name = "picture.png"
					} else if mode == "native-text" {
						name = "document '$;.txt"
					}
					writeFiles(t, dir, name)
					marker := filepath.Join(tools, "opened")
					code := func(label string) string {
						return fmt.Sprintf(`printf '%%s|%%s|%%s|%%s|%%s' %q "$FILE" "$NAME" "$BROWSER_DIR" "$ST_GO_BACKEND" > "$MARKER"`, label)
					}
					for _, tool := range []string{"zat", "govim"} {
						writeDocumentTool(t, tools, tool, `[[ -t 0 && -t 1 && -t 2 ]] || exit 81
if [[ ${0##*/} == govim && $ST_GO_BACKEND == web ]]; then
  [[ $# == 2 && $1 == --web ]] || exit 82
  shift
else
  [[ $# == 1 ]] || exit 83
fi
printf 'native|%s|%s|%s|%s' "$1" "${1##*/}" "$PWD" "$ST_GO_BACKEND" > "$MARKER"`)
					}
					for _, desktop := range []string{"bar", "xdg-open", "st"} {
						writeDocumentTool(t, tools, desktop, code("desktop"))
					}
					if mode == "missing-tool" {
						if err := os.Remove(filepath.Join(tools, "zat")); err != nil {
							t.Fatal(err)
						}
					}
					env := envWithout(testEnv("PATH="+tools, "MARKER="+marker, "ST_GO_BACKEND="+backend,
						"DISPLAY=:999", "WAYLAND_DISPLAY=not-a-server"), "ST_FILE_BROWSER_OPEN")
					args := append([]string{}, program...)
					want := "native"
					switch mode {
					case "legacy-env", "unhandled":
						env = append(env, "ST_FILE_BROWSER_OPEN="+code("legacy"))
						if backend == "x11" {
							want = "legacy"
						}
					case "legacy-flag":
						args = append(args, "--open", code("legacy"))
						if backend == "x11" {
							want = "legacy"
						}
					case "scoped-env":
						for _, platform := range []string{"X11", "FB", "WEB"} {
							env = append(env, "ST_FILE_BROWSER_OPEN_"+platform+"="+code(strings.ToLower(platform)))
						}
						if backend != "unknown" {
							want = backend
						}
					case "scoped-flag":
						args = append(args, "--open-for", "x11", code("x11"), "--open-for=fb", code("fb"), "--open-for", "web", code("web"))
						if backend != "unknown" {
							want = backend
						}
					case "all":
						args = append(args, "--open-for", "all", code("all"))
						want = "all"
					case "disabled":
						args = append(args, "--open-for", "all", "")
						want = "disabled"
					case "missing-tool":
						want = "desktop"
					}
					if (mode == "unhandled" || mode == "missing-tool") && backend != "x11" {
						want = "unregistered"
					}
					f := startBrowserCommand(t, env, append(args, dir)...)
					f.send("\x1b[<0;60;9M\x1b[<0;60;9M")
					if want == "disabled" || want == "unregistered" {
						// Both browsers shorten status text to leave room for hints.
						message := "External open"
						if want == "unregistered" {
							message = "No opener"
						}
						waitContains(t, f, message, "no compatible opener")
						if _, err := os.Stat(marker); !os.IsNotExist(err) {
							t.Fatal("unregistered/disabled opening executed a handler")
						}
					} else {
						file := filepath.Join(dir, name)
						waitFileContent(t, marker, want+"|"+file+"|"+name+"|"+dir+"|"+backend)
					}
					if backend != "x11" && strings.Contains(f.output(), "place bottom-left") {
						t.Fatal("non-X11 opening requested desktop collapse")
					}
				})
			}
		}
	}
}
