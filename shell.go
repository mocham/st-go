package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"st-go/config"
)

// ResolveShell mirrors st's execsh precedence:
//  1. command given with -e
//  2. $SHELL environment variable
//  3. the user's passwd shell
//  4. the shell from config
//
// It also sets the child environment the way st does, and returns the
// resolved program path (used as argv[0]).
func ResolveShell(cfg *config.Config, cmdline string) (prog string, env []string, err error) {
	pw, err := user.Current()
	if err != nil {
		return "", nil, err
	}

	prog = os.Getenv("SHELL")
	if prog == "" {
		prog = passwdShell(pw.Username)
	}
	if prog == "" {
		prog = cfg.Shell
	}
	if cmdline != "" {
		prog = cmdline
	}

	env = stChildEnv(cfg)
	return prog, env, nil
}

// stChildEnv builds the child environment like st's execsh: it unsets
// COLUMNS, LINES and TERMCAP so the child queries the pty size via TIOCGWINSZ
// instead of inheriting a stale size (this is what makes vim lay out its
// screen for the actual terminal rows/cols), then sets the identity and
// configured file-browser icon vars.
func stChildEnv(cfg *config.Config) []string {
	icons := cfg.FileBrowser.Icons
	iconEnv := []struct {
		name  string
		value string
	}{
		{"ST_GO_FILE_BROWSER_ICON_PARENT", icons.Parent},
		{"ST_GO_FILE_BROWSER_ICON_DIRECTORY", icons.Directory},
		{"ST_GO_FILE_BROWSER_ICON_SYMLINK", icons.Symlink},
		{"ST_GO_FILE_BROWSER_ICON_IMAGE", icons.Image},
		{"ST_GO_FILE_BROWSER_ICON_PDF", icons.PDF},
		{"ST_GO_FILE_BROWSER_ICON_TEXT", icons.Text},
		{"ST_GO_FILE_BROWSER_ICON_ARCHIVE", icons.Archive},
		{"ST_GO_FILE_BROWSER_ICON_AUDIO", icons.Audio},
		{"ST_GO_FILE_BROWSER_ICON_VIDEO", icons.Video},
		{"ST_GO_FILE_BROWSER_ICON_CODE", icons.Code},
		{"ST_GO_FILE_BROWSER_ICON_CONFIG", icons.Config},
		{"ST_GO_FILE_BROWSER_ICON_EXECUTABLE", icons.Executable},
		{"ST_GO_FILE_BROWSER_ICON_DEFAULT", icons.Default},
	}

	env := os.Environ()
	drop := []string{"COLUMNS", "LINES", "TERMCAP", "ST_GO_EXECUTABLE", "ST_GO_GEOMETRY_TOKEN"}
	for _, icon := range iconEnv {
		drop = append(drop, icon.name)
	}
	env = envWithout(env, drop...)
	pw, err := user.Current()
	uname, home := "", ""
	if err == nil {
		uname, home = pw.Username, pw.HomeDir
	}
	env = append(env,
		"LOGNAME="+uname,
		"USER="+uname,
		"HOME="+home,
		"TERM="+cfg.Termname,
		"ST_GO_EXECUTABLE="+runningExecutable(),
		"ST_GO_GEOMETRY_TOKEN="+cfg.GeometryToken,
	)
	for _, icon := range iconEnv {
		env = append(env, icon.name+"="+icon.value)
	}
	return env
}

func runningExecutable() string {
	path, err := os.Executable()
	if err != nil {
		return "st"
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// envWithout returns env with the named variables removed (their final value,
// so a later set wins).
func envWithout(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := env[:0:0]
	for _, kv := range env {
		eq := -1
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				eq = i
				break
			}
		}
		if eq > 0 && drop[kv[:eq]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// passwdShell returns the login shell for username from /etc/passwd.
func passwdShell(username string) string {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 7 && f[0] == username {
			return f[6]
		}
	}
	return ""
}
