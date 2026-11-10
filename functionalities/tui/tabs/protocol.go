// Package tabs encodes authenticated terminal-tab requests without display or
// process dependencies. It does not assume every terminal supports tabs.
package tabs

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"st-go/platform_neutral/api"
)

func Capability(token string) bool {
	if len(token) < 32 || len(token) > 128 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func RequestFrame(token, id string, request api.TabSpawnRequest) (string, error) {
	if !Capability(token) || len(id) != 32 {
		return "", fmt.Errorf("invalid tab capability or request ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", fmt.Errorf("invalid tab request ID")
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	payload := base64.RawStdEncoding.EncodeToString(data)
	if len(payload) > api.TabRequestLimit {
		return "", fmt.Errorf("tab request exceeds protocol limit")
	}
	return "\x1bPtab-spawn auth " + token + " " + id + " " + payload + "\x1b\\", nil
}

type Reply struct {
	ID, Status, Message string
	Tab                 uint64
}

func ParseReply(control string) (Reply, bool) {
	if len(control) > 4096 {
		return Reply{}, false
	}
	fields := strings.Fields(control)
	if len(fields) != 4 || fields[0] != "tab-spawn" || len(fields[1]) != 32 {
		return Reply{}, false
	}
	if _, err := hex.DecodeString(fields[1]); err != nil {
		return Reply{}, false
	}
	r := Reply{ID: fields[1], Status: fields[2]}
	switch r.Status {
	case "supported":
		return r, fields[3] == "1"
	case "accepted":
		var err error
		r.Tab, err = strconv.ParseUint(fields[3], 10, 64)
		return r, err == nil && r.Tab != 0
	case "error":
		data, err := base64.RawStdEncoding.DecodeString(fields[3])
		if err != nil || len(data) > 2048 {
			return Reply{}, false
		}
		r.Message = string(data)
		return r, true
	}
	return Reply{}, false
}
