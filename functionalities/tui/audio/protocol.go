// Package audio encodes terminal-owned audio requests and parses their replies.
// It has no native audio, codec, display, or process dependencies.
package audio

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"st-go/platform_neutral/api"
)

func RequestFrame(token, id string, request api.NativeAudioRequest) (string, error) {
	if len(token) < 32 || len(token) > 128 || len(id) != 32 {
		return "", fmt.Errorf("invalid terminal audio capability or job ID")
	}
	for _, value := range []string{token, id} {
		if _, err := hex.DecodeString(value); err != nil {
			return "", fmt.Errorf("invalid terminal audio capability or job ID")
		}
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	payload := base64.RawStdEncoding.EncodeToString(data)
	if len(payload) > 16384 {
		return "", fmt.Errorf("terminal audio request exceeds protocol limit")
	}
	return "\x1bPnative-audio auth " + token + " " + id + " " + payload + "\x1b\\", nil
}

type Reply struct {
	ID, Status, Message string
}

// ParseReply accepts the content of one complete DCS, without its delimiters.
func ParseReply(control string) (Reply, bool) {
	if len(control) > 4096 {
		return Reply{}, false
	}
	fields := strings.Fields(control)
	if len(fields) < 3 || fields[0] != "native-audio" || len(fields[1]) != 32 {
		return Reply{}, false
	}
	if _, err := hex.DecodeString(fields[1]); err != nil {
		return Reply{}, false
	}
	r := Reply{ID: fields[1], Status: fields[2]}
	switch r.Status {
	case "accepted", "done":
		return r, len(fields) == 3
	case "error":
		if len(fields) == 4 {
			data, err := base64.RawStdEncoding.DecodeString(fields[3])
			if err == nil && len(data) <= 2048 {
				r.Message = string(data)
				return r, true
			}
		}
	}
	return Reply{}, false
}
