//go:build linux

package fb

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

func TestNativeVideoConsoleHandoffAndRecovery(t *testing.T) {
	for _, mode := range []string{"success", "player failure", "startup failure", "away", "mode changed", "foreground failure", "closed"} {
		t.Run(mode, func(t *testing.T) {
			d := memoryDriver(t, 40, 40)
			d.running = true
			activeVT := uint16(7)
			var events []string
			c := &console{fd: -1, number: 7, oldKB: 3, changedKB: true, changedVT: true, changedKD: true, changedTermios: true}
			c.ops = consoleOps{pointer: func(req uintptr, value unsafe.Pointer) error {
				if req == vtGetState {
					(*vtState)(value).Active = activeVT
				}
				if req == vtSetMode {
					if (*vtMode)(value).Mode == 0 {
						events = append(events, "VT_AUTO")
					} else {
						events = append(events, "VT_PROCESS")
					}
				}
				return nil
			}, value: func(uintptr, uintptr) error { return nil }, termios: func(*unix.Termios) error { return nil }}
			d.console = c
			d.callbacks = api.Callbacks{Lock: func() {}, Unlock: func() {}, Focus: func(bool) {}, Close: d.Close}
			before := append([]byte(nil), d.screen.memory...)
			pixels := make([]uint32, 1600)
			for i := range pixels {
				pixels[i] = 0xff224466
			}
			d.nativeOps = &nativeVideoOps{
				openTTY: func() (*os.File, error) { return os.CreateTemp(t.TempDir(), "fake-tty") },
				restoreForeground: func() error {
					events = append(events, "foreground")
					if mode == "foreground failure" {
						return errors.New("foreground failed")
					}
					return nil
				},
				run: func(api.NativeVideoRequest, *os.File) error {
					events = append(events, "player")
					if d.active || c.changedVT || c.changedKB || c.changedKD || c.changedTermios {
						t.Fatal("player started before console/input/painting release")
					}
					d.Upload(pixels, 40, 0, 0, 40, 40)
					if !bytes.Equal(before, d.screen.memory) {
						t.Fatal("host painted over native video")
					}
					if mode == "away" {
						activeVT = 8
					}
					if mode == "mode changed" {
						d.screen.current = func() (layout, error) { l := d.screen.layout; l.width++; return l, nil }
					}
					if mode == "closed" {
						d.Close()
					}
					if mode == "player failure" || mode == "startup failure" {
						return errors.New(mode)
					}
					return nil
				},
			}
			err := d.runNativeVideo(api.NativeVideoRequest{})
			wantErr := mode == "player failure" || mode == "startup failure" || mode == "mode changed" || mode == "foreground failure"
			if (err != nil) != wantErr {
				t.Fatal("incorrect player/recovery error", err)
			}
			joined := strings.Join(events, ",")
			if !strings.HasPrefix(joined, "VT_AUTO,player,foreground,VT_AUTO") {
				t.Fatal("incorrect console handoff sequence", joined)
			}
			if mode == "away" || mode == "mode changed" || mode == "foreground failure" || mode == "closed" {
				if d.active || !bytes.Equal(before, d.screen.memory) {
					t.Fatal("unsafe repaint after video")
				}
			} else if !d.active || pixelAt(d, 0, 0) != 0x224466 {
				t.Fatal("native video did not restore retained terminal pixels")
			}
			if (mode == "mode changed" || mode == "foreground failure") && !d.closed {
				t.Fatal("unsafe console recovery did not close host")
			}
		})
	}
}

func TestNativeVideoQueueRejectsBusyOrInvalidRequests(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	d.running = true
	dir := t.TempDir()
	media, config := filepath.Join(dir, "movie.mp4"), filepath.Join(dir, "alsa.conf")
	for _, path := range []string{media, config} {
		if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := api.NativeVideoRequest{File: media, ALSAConfig: config, AudioDevice: "alsa/plughw:CARD=1,DEV=0"}
	if err := d.StartNativeVideo(r, func(error) {}); err != nil {
		t.Fatal(err)
	}
	if err := d.StartNativeVideo(r, func(error) {}); err == nil {
		t.Fatal("accepted concurrent native players")
	}
	d.nativeBusy = false
	r.File = "/dev/zero"
	if err := d.StartNativeVideo(r, func(error) {}); err == nil {
		t.Fatal("accepted a device instead of a regular media file")
	}
}

func TestNativePlayerCommandUsesDetectedConsoleNotPTY(t *testing.T) {
	tty, err := os.CreateTemp(t.TempDir(), "fake-console")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	r := api.NativeVideoRequest{File: "/media/movie ';\n.mp4", ALSAConfig: "/private/alsa.conf", AudioDevice: "alsa/plughw:1,0", Volume: 33}
	cmd := nativePlayerCommand(r, tty)
	want := []string{"mpv", "--vo=drm", "--hwdec=auto-copy", "--ao=alsa", "--audio-device=alsa/plughw:1,0", "--volume=33", "--", r.File}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatal("incorrect native player argv", cmd.Args)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Foreground || cmd.SysProcAttr.Setsid || cmd.SysProcAttr.Setctty || cmd.SysProcAttr.Ctty != int(tty.Fd()) {
		t.Fatal("player would inherit a PTY/wrong controlling session", cmd.SysProcAttr)
	}
	if cmd.Stdin != tty || cmd.Stdout != tty || cmd.Stderr != tty {
		t.Fatal("player stdio was not attached to the physical console")
	}
}
