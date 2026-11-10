package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func usage() {
	fmt.Print(`Usage: file-browser [options] [directory]

Options:
  -a, --hidden       include hidden entries
  -o, --open CODE    legacy custom opener (X11 only)
  --open-for PLATFORM CODE
                    register an opener for x11, fb, web, or all (repeatable)
  -h, --help         show this help

CODE is evaluated by ` + "`bash -c`" + ` with FILE, NAME, and BROWSER_DIR exported.
It may contain a complete shell command sequence or function definitions.
By default, images open in goiv, .pdf opens in zat, and text/source/config files open in govim when available in PATH.
Supported st-go hosts open them in new focused tabs, initializing the
tab bar lazily. Without tab support, they use this terminal and return on exit.
Ctrl+Shift+PageUp/PageDown switches tabs; Ctrl+Shift+W closes the current tab.
Otherwise the
external fallbacks are used on X11 only. Matching explicit openers take precedence.
ST_FILE_BROWSER_OPEN_X11, _FB, _WEB, or _ALL register environment openers.
Legacy ST_FILE_BROWSER_OPEN is X11-only. ST_GO_BACKEND identifies the terminal.
Framebuffer video uses mpv --vo=drm with --audio-device=alsa/plughw:1,0 by default.
There is no default X11/web video player; matching custom openers still override.

Keys: arrows navigate, Enter opens/enters, Backspace goes up, PgUp/PgDn
scroll, Ctrl+D/Ctrl+U page with one overlapping row, [ and ] change PDF or
rendered document pages, / enters a path, : runs a shell command
(with $F = selected file and $D = current directory), :s/old/new/[g] renames
matching entries, :help shows this manual, Tab shows/hides the navigation
sidebar, Shift+Tab toggles floating/pinned navigation, . toggles hidden files,
r refreshes, q quits.
Mouse: click selects, double-click opens/enters, wheel scrolls the list; over a
PDF or rendered document preview the wheel changes pages.
DOCX and XLSX have preliminary text-only, paginated previews from their ZIP XML.
Legacy DOC previews support unencrypted Word 97–2003 binary files without external tools.
SQLite .sqlite3, .sqlite, .db, .db3, and .s3db files open as read-only virtual
directories: tables contain paginated record entries.
`)
}

// parseArgs mirrors the shell argument loop. It returns the settings, whether
// parsing is finished (help/--), and the exit code when finished.
func parseArgs(args []string) (showHidden bool, openCmd string, customOpen bool, startDir string, done bool, code int) {
	showHidden = parseShowHidden()
	openCmd, customOpen = defaultOpenCommand()
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-a" || a == "--hidden":
			showHidden = true
		case a == "-o" || a == "--open":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "file-browser: --open requires shell code")
				return showHidden, openCmd, customOpen, startDir, true, 2
			}
			i++
			if openPlatformMatches("x11") {
				openCmd = args[i]
				customOpen = true
			}
		case strings.HasPrefix(a, "--open="):
			if openPlatformMatches("x11") {
				openCmd = strings.TrimPrefix(a, "--open=")
				customOpen = true
			}
		case a == "--open-for" || strings.HasPrefix(a, "--open-for="):
			platform := strings.TrimPrefix(a, "--open-for=")
			if a == "--open-for" {
				if i+2 >= len(args) {
					fmt.Fprintln(os.Stderr, "file-browser: --open-for requires PLATFORM and shell code")
					return showHidden, openCmd, customOpen, startDir, true, 2
				}
				i++
				platform = args[i]
			}
			platform = strings.ToLower(platform)
			if !validOpenPlatform(platform) || i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "file-browser: --open-for requires x11, fb, web, or all and shell code")
				return showHidden, openCmd, customOpen, startDir, true, 2
			}
			i++
			if openPlatformMatches(platform) {
				openCmd, customOpen = args[i], true
			}
		case a == "-h" || a == "--help":
			usage()
			return showHidden, openCmd, customOpen, startDir, true, 0
		case a == "--":
			rest := args[i+1:]
			if len(rest) > 1 {
				fmt.Fprintln(os.Stderr, "file-browser: only one directory may be specified")
				return showHidden, openCmd, customOpen, startDir, true, 2
			}
			if len(rest) == 1 {
				startDir = rest[0]
			}
			return showHidden, openCmd, customOpen, startDir, false, 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "file-browser: unknown option: %s\n", a)
			return showHidden, openCmd, customOpen, startDir, true, 2
		default:
			if startDir != "" {
				fmt.Fprintln(os.Stderr, "file-browser: only one directory may be specified")
				return showHidden, openCmd, customOpen, startDir, true, 2
			}
			startDir = a
		}
	}
	return showHidden, openCmd, customOpen, startDir, false, 0
}

func run() int {
	if len(os.Args) == 3 && os.Args[1] == "--native-video" {
		return runNativeVideoHelper(os.Args[2])
	}
	showHidden, openCmd, customOpen, startDir, done, code := parseArgs(os.Args[1:])
	if done {
		return code
	}
	if startDir == "" {
		startDir, _ = os.Getwd()
	}
	dir, err := resolveDir(startDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "file-browser: cannot open directory: %s\n", startDir)
		return 1
	}

	b := &Browser{}
	b.fd = int(os.Stdin.Fd())
	b.in = &input{fd: b.fd}
	b.out = bufio.NewWriter(os.Stdout)
	b.dir = dir
	b.showHidden = showHidden
	b.openCmd = openCmd
	b.customOpen = customOpen
	b.sigCh = make(chan os.Signal, 8)
	signal.Notify(b.sigCh, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	b.setup()
	b.makeRaw()
	defer b.cleanup()
	b.start()
	b.loop()
	return b.exitCode
}

func main() {
	os.Exit(run())
}
