package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

func videoAudioDevice(getenv func(string) string) (string, error) {
	if device := getenv("ST_FILE_BROWSER_ALSA_DEVICE"); device != "" {
		if !strings.HasPrefix(device, "alsa/") {
			device = "alsa/" + device
		}
		return device, nil
	}
	card := getenv("ALSA_PCM_CARD")
	if card == "" {
		card = getenv("ALSA_CARD")
	}
	device := getenv("ALSA_PCM_DEVICE")
	if card == "" && device == "" {
		return "alsa/plughw:1,0", nil
	}
	if card == "" {
		card = "1"
	}
	if device == "" {
		device = "0"
	}
	for _, ch := range card {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return "", fmt.Errorf("invalid ALSA card identifier")
		}
	}
	n, err := strconv.Atoi(device)
	if err != nil || n < 0 || n > 1023 {
		return "", fmt.Errorf("invalid ALSA device index")
	}
	return fmt.Sprintf("alsa/plughw:CARD=%s,DEV=%d", card, n), nil
}

func runNativeVideoHelper(path string) int {
	if err := nativeVideoHelper(path); err != nil {
		fmt.Fprintf(os.Stderr, "native video: %q\n", err.Error())
		return 1
	}
	return 0
}

// Volume is an mpv option only. Do not change global ALSA controls or the
// MP3 open/send path while preparing a different player.
func playbackVolume(value string) (int, error) {
	if value == "" {
		return 33, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("ST_FILE_BROWSER_VOLUME must be 0..100")
	}
	return n, nil
}

// This helper is a terminal client, not a platform launcher: only an
// authenticated ANSI/DCS request crosses to the host that owns the real VT.
func nativeVideoHelper(path string) error {
	token := os.Getenv("ST_GO_NATIVE_VIDEO_TOKEN")
	if os.Getenv("ST_GO_BACKEND") != "fb" || os.Getenv("ST_GO_NATIVE_VIDEO") != "1" || token == "" {
		return fmt.Errorf("native console handoff unavailable; restart the rebuilt st-fb")
	}
	if _, err := hex.DecodeString(token); err != nil || len(token) > 128 {
		return fmt.Errorf("invalid native video capability")
	}
	file, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	cache, err := os.MkdirTemp("", "st-go-native-video.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	configPath, err := videoALSAConfig(cache)
	if err != nil {
		return err
	}
	device, err := videoAudioDevice(os.Getenv)
	if err != nil {
		return err
	}
	percent, err := playbackVolume(os.Getenv("ST_FILE_BROWSER_VOLUME"))
	if err != nil {
		return err
	}
	request := api.NativeVideoRequest{File: file, ALSAConfig: configPath, AudioDevice: device, Volume: percent}
	if err := request.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.ICRNL | unix.INLCR | unix.IGNCR | unix.IXON | unix.ISTRIP
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return err
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, old)
	payload := base64.RawStdEncoding.EncodeToString(data)
	if len(payload) > 32768 {
		return fmt.Errorf("native video request is too large")
	}
	if _, err := fmt.Fprintf(os.Stdout, "\x1bPnative-video auth %s %s %s\x1b\\", token, id, payload); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	accepted := false
	var pending []byte
	prefix := []byte("\x1bPnative-video " + id + " ")
	for {
		if !accepted && time.Now().After(deadline) {
			return fmt.Errorf("native console did not acknowledge the handoff")
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil && err != unix.EINTR {
			return err
		}
		if fds[0].Revents == 0 {
			continue
		}
		var buf [4096]byte
		n, err := unix.Read(fd, buf[:])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("terminal disconnected during native video")
		}
		pending = append(pending, buf[:n]...)
		if len(pending) > 16384 {
			return fmt.Errorf("native video reply exceeded its limit")
		}
		for {
			start := bytes.Index(pending, prefix)
			if start < 0 {
				break
			}
			end := bytes.Index(pending[start:], []byte("\x1b\\"))
			if end < 0 {
				break
			}
			fields := strings.Fields(string(pending[start+len(prefix) : start+end]))
			pending = pending[start+end+2:]
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "accepted":
				accepted = true
			case "done":
				return nil
			case "error":
				if len(fields) == 2 {
					message, err := base64.RawStdEncoding.DecodeString(fields[1])
					if err == nil {
						return fmt.Errorf("%s", message)
					}
				}
				return fmt.Errorf("native video failed")
			}
		}
	}
}
