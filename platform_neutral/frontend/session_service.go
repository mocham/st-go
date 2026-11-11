package frontend

import (
	"crypto/sha256"
	"os"
	"path/filepath"

	"st-go/config"
	"st-go/limits"
	"st-go/platform_neutral/api"
	"st-go/widgets"
)

func serveTerminalSessions(service api.SessionService, cfg *config.Config, ratio float64, command []string, directory, title string, tabs bool) error {
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	shell, _, err := ResolveShell(cfg, "")
	if err != nil {
		return err
	}
	defaults := api.TabSpawnRequest{Command: []string{shell}, Directory: directory}
	initial := defaults
	initial.Title = title
	if len(command) != 0 {
		initial.Command = command
	}
	factory := func(host api.Host, request api.TabSpawnRequest, spawn api.SessionSpawn) (func(), error) {
		local := *cfg
		local.GeometryToken, local.NativeAudioToken = newGeometryToken(), newGeometryToken()
		local.ClipboardSyncToken, local.NativeVideoToken, local.TabSpawnToken = "", "", ""
		if tabs {
			local.TabSpawnToken = newGeometryToken()
		}
		widget, err := widgets.Wrap(host)
		if err != nil {
			return nil, err
		}
		t, err := NewTerminalWidget(&local, widget, ratio)
		if err != nil {
			widget.Close()
			return nil, err
		}
		if local.TabSpawnToken != "" {
			t.tabs = &serviceTabs{spawnRequest: spawn}
		}
		title := request.Title
		if title == "" {
			title = filepath.Base(request.Command[0])
		}
		t.setTitle(title)
		command := append([]string(nil), request.Command...)
		cleanup, err := prepareSessionFile(command, request.CopyFile)
		if err != nil {
			t.Close()
			return nil, err
		}
		t.sessionCleanup = cleanup
		if err := t.StartSession(SessionOptions{Command: command, Directory: request.Directory}); err != nil {
			if cleanup != nil {
				cleanup(nil)
			}
			t.Close()
			return nil, err
		}
		widget.Show()
		go t.Run()
		return func() {
			// Revoke I/O before taking the parser lock: a child that stopped
			// reading must not block service deletion/shutdown in a PTY write.
			t.session.close()
			t.closeSession()
			<-t.session.exited
		}, nil
	}
	return service.ServeSessions(factory, initial, defaults)
}

// Archive openers hand the new terminal its own copy before acknowledging exec.
func prepareSessionFile(command []string, index *int) (func(error), error) {
	if index == nil {
		return nil, nil
	}
	data, err := limits.ReadFile(command[*index])
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "st-go-tab-file-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, filepath.Base(command[*index]))
	if err := os.WriteFile(path, data, 0600); err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	command[*index] = path
	digest := sha256.Sum256(data)
	return func(waitErr error) {
		current, readErr := limits.ReadFile(path)
		entries, dirErr := os.ReadDir(directory)
		if waitErr != nil || readErr != nil || dirErr != nil || len(entries) != 1 || sha256.Sum256(current) != digest {
			limits.Report("tab-file", "retained edited or interrupted copy %q", directory)
			return
		}
		os.RemoveAll(directory)
	}, nil
}

type serviceTabs struct{ spawnRequest api.SessionSpawn }

func (m *serviceTabs) spawn(t *Terminal, id string, request api.TabSpawnRequest) bool {
	if m.spawnRequest == nil {
		return false
	}
	return m.spawnRequest(id, request, func(tab api.WidgetID, err error) {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.tabReply(id, tab, err)
	})
}
func (*serviceTabs) titleChanged(api.WidgetID, string) {}
func (*serviceTabs) terminalClosed(api.WidgetID)       {}
func (*serviceTabs) run(t *Terminal)                   { t.run() }

var _ terminalTabManager = (*serviceTabs)(nil)
