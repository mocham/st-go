package main

import (
	"os"
	"os/user"
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

	env = append(os.Environ(),
		"LOGNAME="+pw.Username,
		"USER="+pw.Username,
		"SHELL="+prog,
		"HOME="+pw.HomeDir,
		"TERM="+cfg.Termname,
	)
	return prog, env, nil
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
