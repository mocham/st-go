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
  -o, --open CODE    run CODE on a file double-click or Enter
  -h, --help         show this help

CODE is evaluated by ` + "`bash -c`" + ` with FILE, NAME, and BROWSER_DIR exported.
It may contain a complete shell command sequence or function definitions.

Keys: arrows navigate, Enter opens/enters, Backspace goes up, PgUp/PgDn
scroll, [ and ] change PDF pages, / enters a path, : runs a shell command
(with $F = selected file and $D = current directory), :s/old/new/[g] renames
matching entries, :help shows this manual, . toggles hidden files, r refreshes,
q quits.
Mouse: click selects, double-click opens/enters, wheel scrolls the list; over a
PDF preview the wheel changes pages.
`)
}

// parseArgs mirrors the shell argument loop. It returns the settings, whether
// parsing is finished (help/--), and the exit code when finished.
func parseArgs(args []string) (showHidden bool, openCmd string, startDir string, done bool, code int) {
	showHidden = parseShowHidden()
	openCmd = defaultOpenCommand()
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-a" || a == "--hidden":
			showHidden = true
		case a == "-o" || a == "--open":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "file-browser: --open requires shell code")
				return showHidden, openCmd, startDir, true, 2
			}
			i++
			openCmd = args[i]
		case strings.HasPrefix(a, "--open="):
			openCmd = strings.TrimPrefix(a, "--open=")
		case a == "-h" || a == "--help":
			usage()
			return showHidden, openCmd, startDir, true, 0
		case a == "--":
			rest := args[i+1:]
			if len(rest) > 1 {
				fmt.Fprintln(os.Stderr, "file-browser: only one directory may be specified")
				return showHidden, openCmd, startDir, true, 2
			}
			if len(rest) == 1 {
				startDir = rest[0]
			}
			return showHidden, openCmd, startDir, false, 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "file-browser: unknown option: %s\n", a)
			return showHidden, openCmd, startDir, true, 2
		default:
			if startDir != "" {
				fmt.Fprintln(os.Stderr, "file-browser: only one directory may be specified")
				return showHidden, openCmd, startDir, true, 2
			}
			startDir = a
		}
	}
	return showHidden, openCmd, startDir, false, 0
}

func run() int {
	showHidden, openCmd, startDir, done, code := parseArgs(os.Args[1:])
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
