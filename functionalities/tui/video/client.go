// Package video is the PTY client for terminal-owned composited MP4 playback.
package video

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"st-go/platform_neutral/api"
)

type Client struct {
	out   io.Writer
	token string
	ID    string
}

func New(out io.Writer, token string) (*Client, error) {
	if token == "" || len(token) > 256 || strings.ContainsAny(token, " \t\r\n\x1b\x00") {
		return nil, fmt.Errorf("run gov inside the updated st-fb terminal")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &Client{out: out, token: token, ID: hex.EncodeToString(id[:])}, nil
}
func (c *Client) Send(r api.VideoRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.out, "\x1bPvideo auth %s %s %s\x1b\\", c.token, c.ID, base64.RawStdEncoding.EncodeToString(data))
	return err
}
func (c *Client) Reply(payload string) (api.VideoStatus, bool) {
	var s api.VideoStatus
	fields := strings.Fields(payload)
	if len(fields) != 3 || fields[0] != "video" || fields[1] != c.ID || len(fields[2]) > 16384 {
		return s, false
	}
	data, err := base64.RawStdEncoding.DecodeString(fields[2])
	if err != nil || json.Unmarshal(data, &s) != nil {
		return s, false
	}
	return s, true
}
