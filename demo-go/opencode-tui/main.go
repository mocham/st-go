// opencode-tui is a small OpenCode V2 client with an st-go rich Markdown/LaTeX pane.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type options struct {
	server       string
	session      string
	previewWidth int
	noPreview    bool
	web          bool
	directory    string
}

func parseOptions(args []string) (options, error) {
	var result options
	flags := flag.NewFlagSet("opencode-tui", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&result.server, "server", os.Getenv("OPENCODE_TUI_SERVER"), "running OpenCode V2 URL or host:port (or set OPENCODE_TUI_SERVER); bare addresses use HTTP")
	flags.StringVar(&result.session, "session", "", "resume an existing session ID")
	flags.IntVar(&result.previewWidth, "preview-width", 58, "rich response pane width percent")
	flags.BoolVar(&result.noPreview, "no-preview", false, "disable the st-go rich response pane")
	flags.BoolVar(&result.web, "web", false, "render rich responses as browser-native HTML in st-web")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: opencode-tui --server URL [options] [DIRECTORY]")
		fmt.Fprintln(flags.Output(), "\nMinimal OpenCode V2 TUI with an st-go Markdown/LaTeX response pane.")
		fmt.Fprintln(flags.Output(), "Connect to a running server with --server or OPENCODE_TUI_SERVER.")
		fmt.Fprintln(flags.Output(), "DIRECTORY is a project path on the server; it defaults to the current directory.")
		fmt.Fprintln(flags.Output(), "For Basic auth, set OPENCODE_TUI_PASSWORD (username defaults to opencode).")
		fmt.Fprintln(flags.Output(), "OPENCODE_TUI_AUTHORIZATION overrides password auth with a complete Authorization header.")
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return result, err
	}
	result.server = strings.TrimSpace(result.server)
	if result.server == "" {
		return result, fmt.Errorf("server URL required: use --server URL or set OPENCODE_TUI_SERVER")
	}
	if flags.NArg() > 1 {
		return result, fmt.Errorf("expected at most one DIRECTORY")
	}
	if result.previewWidth < 20 || result.previewWidth > 80 {
		return result, fmt.Errorf("preview width must be between 20 and 80 percent")
	}
	directory := "."
	if flags.NArg() == 1 {
		directory = flags.Arg(0)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return result, err
	}
	// The HTTP server validates project paths. A remote project need not exist
	// on the machine hosting this terminal client.
	result.directory = absolute
	return result, nil
}

func serverAuthorization() string {
	if header := os.Getenv("OPENCODE_TUI_AUTHORIZATION"); header != "" {
		return header
	}
	password, present := os.LookupEnv("OPENCODE_TUI_PASSWORD")
	if !present {
		return ""
	}
	username := os.Getenv("OPENCODE_TUI_USERNAME")
	if username == "" {
		username = "opencode"
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func run(args []string) int {
	configuration, err := parseOptions(args)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 2
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authorization := serverAuthorization()
	client, err := newAPIClient(configuration.server, authorization)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 2
	}
	if err := client.info(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui: connect to OpenCode server:", err)
		return 1
	}

	var session sessionInfo
	if configuration.session == "" {
		session, err = client.createSession(ctx, configuration.directory)
	} else {
		session, err = client.getSession(ctx, configuration.session)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 1
	}
	snapshot, err := client.snapshot(ctx, session.ID, 10)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 1
	}
	rich, err := newRichPreview(os.Stdout, configuration.previewWidth, !configuration.noPreview, configuration.web)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 1
	}
	defer rich.close()
	application := newApp(client, *snapshot.session, snapshot.messages, snapshot.permissions, os.Stdout, int(os.Stdin.Fd()), rich)
	application.historyCursor, application.busy = snapshot.messageCursor, snapshot.busy
	application.replaceSessionActivity(snapshot.active)
	if err := application.run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencode-tui:", err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:])) }
