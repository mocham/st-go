package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func usage() {
	fmt.Print(`Usage: gols [DATABASE|DIRECTORY]...

Browse immutable GoLS SQLite corpus databases directly in an st-go terminal.
Directories are searched recursively for .sqlite3, .sqlite, and .db files.
With no arguments, the recently opened databases from ~/.cache/gols.json are
shown. Opening a database moves it to the front of that list.

Hierarchy: database -> corpus -> documents -> PDF/LaTeX contents

Keys: arrows navigate, Enter/right opens a level, Backspace/left goes back,
PgUp/PgDn scroll, / searches, r reopens databases, [ and ] change rendered TeX
pages, and Tab hides or focuses the navigation pane. Mouse clicks and wheel
scrolling are supported.
`)
}

func parseArgs(args []string) (paths []string, done bool, code int) {
	literal := false
	for _, argument := range args {
		switch {
		case !literal && (argument == "-h" || argument == "--help"):
			usage()
			return nil, true, 0
		case !literal && argument == "--":
			literal = true
		case !literal && strings.HasPrefix(argument, "-"):
			fmt.Fprintf(os.Stderr, "gols: unknown option: %s\n", argument)
			return nil, true, 2
		default:
			paths = append(paths, argument)
		}
	}
	return paths, false, 0
}

func databaseCandidates(inputs []string) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	add := func(file string) {
		absolute, err := filepath.Abs(file)
		if err != nil || seen[absolute] {
			return
		}
		seen[absolute] = true
		result = append(result, absolute)
	}
	for _, inputPath := range inputs {
		if strings.TrimSpace(inputPath) == "" {
			continue
		}
		info, err := os.Stat(inputPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", inputPath, err)
		}
		if !info.IsDir() {
			add(inputPath)
			continue
		}
		err = filepath.WalkDir(inputPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != inputPath {
					name := entry.Name()
					if strings.HasPrefix(name, ".") || name == "third_party" || name == "node_modules" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension == ".sqlite3" || extension == ".sqlite" || extension == ".db" {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

func run() int {
	paths, done, code := parseArgs(os.Args[1:])
	if done {
		return code
	}
	recentMode := len(paths) == 0
	startupWarning := ""
	var (
		candidates []string
		err        error
	)
	if recentMode {
		candidates, err = loadRecentDatabases()
		if err != nil {
			startupWarning = "Could not read recent databases: " + err.Error()
			candidates = nil
		}
	} else {
		candidates, err = databaseCandidates(paths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gols: %v\n", err)
			return 1
		}
	}
	var corpora []*corpus
	var openedPaths []string
	for _, candidate := range candidates {
		opened, err := openCorpus(candidate)
		if err != nil {
			if !recentMode && len(candidates) == 1 {
				fmt.Fprintf(os.Stderr, "gols: %s: %v\n", candidate, err)
				return 1
			}
			continue
		}
		corpora = append(corpora, opened)
		openedPaths = append(openedPaths, opened.file)
	}
	if !recentMode && len(corpora) == 0 {
		fmt.Fprintln(os.Stderr, "gols: no GoLS corpus databases found")
		return 1
	}
	if !recentMode && len(openedPaths) > 0 {
		if err := recordRecentDatabases(openedPaths); err != nil {
			startupWarning = "Could not update recent databases: " + err.Error()
		}
	}

	browser := &Browser{
		fd:             int(os.Stdin.Fd()),
		out:            bufio.NewWriter(os.Stdout),
		corpora:        corpora,
		recentMode:     recentMode,
		recentRecorder: recordRecentDatabases,
		sigCh:          make(chan os.Signal, 8),
	}
	browser.in = &input{fd: browser.fd}
	signal.Notify(browser.sigCh, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	if err := browser.setup(); err != nil {
		fmt.Fprintf(os.Stderr, "gols: %v\n", err)
		for _, corpus := range corpora {
			corpus.close()
		}
		return 1
	}
	if startupWarning != "" {
		browser.status = startupWarning
	} else if recentMode && len(corpora) == 0 {
		browser.status = "No recent databases — run gols DATABASE to add one"
	} else if recentMode {
		browser.status = fmt.Sprintf("Recent databases: %d", len(corpora))
	}
	browser.makeRaw()
	defer browser.cleanup()
	browser.start()
	browser.loop()
	return browser.exitCode
}

func main() { os.Exit(run()) }
