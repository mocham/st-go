//go:build linux

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
	"st-go/ptyutil"
)

func TestPTYHandoffAndTermiosRestoration(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			master, slave, err := ptyutil.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			old, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ST_GO_NATIVE_VIDEO", "1")
			t.Setenv("ST_GO_NATIVE_VIDEO_TOKEN", "aabbccdd")
			o := options{file: "/media/movie ';\n世界.mp4", muted: true, volume: 40, device: "/dev/dri/card1", connector: 42}
			host := make(chan error, 1)
			go func() {
				var frame strings.Builder
				for !strings.HasSuffix(frame.String(), "\x1b\\") {
					var b [1]byte
					if _, err := io.ReadFull(master, b[:]); err != nil {
						host <- err
						return
					}
					frame.WriteByte(b[0])
				}
				fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(frame.String(), "\x1bP"), "\x1b\\"))
				if len(fields) != 5 || fields[0] != "native-video" || fields[1] != "auth" || fields[2] != "aabbccdd" {
					host <- fmt.Errorf("bad request: %q", frame.String())
					return
				}
				data, err := base64.RawStdEncoding.DecodeString(fields[4])
				var request api.NativeVideoRequest
				if err != nil || json.Unmarshal(data, &request) != nil || request != o.request() {
					host <- fmt.Errorf("request changed: %+v", request)
					return
				}
				id := fields[3]
				frames := "\x1bPnative-video stale error\x1b\\\x1bPnative-video " + id + " accepted\x1b\\"
				if fail {
					frames += "\x1bPnative-video " + id + " error " + base64.RawStdEncoding.EncodeToString([]byte("DRM unavailable")) + "\x1b\\"
				} else {
					frames += "\x1bPnative-video " + id + " done\x1b\\"
				}
				for _, b := range []byte(frames) {
					if _, err := master.Write([]byte{b}); err != nil {
						host <- err
						return
					}
				}
				host <- nil
			}()
			err = handoff(o, slave, slave)
			if hostErr := <-host; hostErr != nil {
				t.Fatal(hostErr)
			}
			if (err != nil) != fail || (fail && err.Error() != "DRM unavailable") {
				t.Fatal(err)
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil || *after != *old {
				t.Fatal("PTY termios was not restored", err)
			}
		})
	}
}
