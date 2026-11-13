package frontend

import (
	"bytes"
	"errors"
	"flag"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

type lifecycleHost struct {
	api.Host
	callbacks api.Callbacks
	done      chan struct{}
	once      sync.Once
	marker    string
}

func (h *lifecycleHost) Bind(c api.Callbacks)                     { h.callbacks = c }
func (h *lifecycleHost) Show()                                    {}
func (h *lifecycleHost) BackendName() string                      { return "fb" }
func (h *lifecycleHost) SetupKeys()                               {}
func (h *lifecycleHost) SetTitle(string)                          {}
func (h *lifecycleHost) SetIconTitle(string)                      {}
func (h *lifecycleHost) Upload([]uint32, int, int, int, int, int) {}
func (h *lifecycleHost) ClientSize() (int, int, error)            { return 482, 282, nil }
func (h *lifecycleHost) Close() {
	h.once.Do(func() { os.WriteFile(h.marker, []byte("restored"), 0600); close(h.done) })
}
func (h *lifecycleHost) Run() { <-h.done }
func (*lifecycleHost) NewSurface(image.Rectangle, uint32) (api.Surface, error) {
	return nil, errors.New("lifecycle-only host has no surfaces")
}

func TestSessionLifecycleHelper(t *testing.T) {
	marker := os.Getenv("ST_TEST_LIFECYCLE_MARKER")
	if marker == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("session-helper", flag.ExitOnError)
	command := []string{"/bin/sh", "-c", "stty size > \"$ST_TEST_LIFECYCLE_SIZE\"\nprintf '%s' \"$ST_GO_BACKEND\" > \"$ST_TEST_LIFECYCLE_BACKEND\""}
	if os.Getenv("ST_TEST_LIFECYCLE_BAD_SPAWN") == "1" {
		command = []string{"/nonexistent/st-go-test-program"}
	}
	args := []string{os.Args[0]}
	if ratio := os.Getenv("ST_TEST_LIFECYCLE_RATIO"); ratio != "" {
		args = append(args, "-ratio", ratio)
	}
	os.Args = append(append(args, "-e"), command...)
	font, err := os.ReadFile("Monaco_Linux.ttf")
	if err != nil {
		t.Fatal(err)
	}
	options := StartupOptions{}
	if scale := os.Getenv("ST_TEST_LIFECYCLE_SCALE"); scale != "" {
		options.FontScale = func() (float64, error) { return strconv.ParseFloat(scale, 64) }
	}
	MainWithOptions(func(api.WindowOptions) (api.Host, error) {
		return &lifecycleHost{done: make(chan struct{}), marker: marker}, nil
	}, font, options)
	os.Exit(0)
}

func TestSessionExitAndSpawnFailureRestoreHost(t *testing.T) {
	for _, test := range []struct {
		name, scale, ratio, size string
		badSpawn                 bool
	}{
		{name: "child-exit", size: "10 30\n"},
		{name: "spawn-failure", badSpawn: true},
		{name: "scaled-display", scale: "0.8", size: "12 40\n"},
		{name: "scaled-display-ratio", scale: "0.8", ratio: "1.25", size: "10 30\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "restored")
			size := filepath.Join(dir, "size")
			backend := filepath.Join(dir, "backend")
			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLifecycleHelper$")
			cmd.Env = append(os.Environ(), "ST_TEST_LIFECYCLE_MARKER="+marker, "ST_TEST_LIFECYCLE_SIZE="+size, "ST_TEST_LIFECYCLE_BACKEND="+backend, "ST_GO_BACKEND=x11")
			cmd.Env = append(cmd.Env, "ST_TEST_LIFECYCLE_SCALE="+test.scale, "ST_TEST_LIFECYCLE_RATIO="+test.ratio)
			if test.badSpawn {
				cmd.Env = append(cmd.Env, "ST_TEST_LIFECYCLE_BAD_SPAWN=1")
			}
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if !test.badSpawn && err != nil {
					t.Fatalf("helper: %v\n%s", err, output.String())
				}
			case <-time.After(6 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("host did not stop after child exit")
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "restored" {
				t.Fatalf("host restoration skipped: %v\n%s", err, output.String())
			}
			if !test.badSpawn {
				data, err := os.ReadFile(size)
				if err != nil || string(data) != test.size {
					t.Fatalf("child's initial size=%q, %v", data, err)
				}
				data, err = os.ReadFile(backend)
				if err != nil || string(data) != "fb" {
					t.Fatalf("child inherited stale backend identity: %q, %v", data, err)
				}
			}
		})
	}
}
