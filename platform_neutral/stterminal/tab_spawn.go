package stterminal

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"

	"st-go/platform_neutral/api"
)

type TabSpawnHooks interface {
	SpawnTab(id string, request api.TabSpawnRequest)
}

func (t *Term) dslTabSpawn(args []string) {
	if len(args) != 4 || args[0] != "auth" || t.cfg.TabSpawnToken == "" ||
		subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.TabSpawnToken)) != 1 ||
		len(args[2]) != 32 || len(args[3]) > api.TabRequestLimit {
		return
	}
	if _, err := hex.DecodeString(args[2]); err != nil {
		return
	}
	data, err := base64.RawStdEncoding.DecodeString(args[3])
	if err != nil {
		return
	}
	var request api.TabSpawnRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Validate() != nil {
		return
	}
	if hooks, ok := t.hooks.(TabSpawnHooks); ok {
		hooks.SpawnTab(args[2], request)
	}
}
