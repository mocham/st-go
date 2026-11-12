package main

import (
	"encoding/json"
	"time"

	"st-go/platform_neutral/api"
)

const (
	maxResource = 64 << 20
	maxMessage  = 64 << 20
	maxCells    = 200000
)

// These are the public JSON shapes used by st-web. Keeping the envelope here
// avoids importing the graphical host into this independently built TUI.
type scene struct {
	ID                  uint64 `json:"id"`
	X, Y, Width, Height int
	Bar, Hidden         bool
	Update              api.SceneUpdate
}

type overlay struct {
	ID                  uint64
	X, Y, Width, Height int
	Document            string
	View                api.HTMLView
}

type mediaOverlay struct {
	ID                  uint64
	X, Y, Width, Height int
	View                api.MediaView
}

type documentRequest struct {
	ID, Media uint64
	Query     api.DocumentQuery
	deadline  time.Time
}

type serverMessage struct {
	Type                                         string
	Width, Height, CellWidth, CellHeight, Border int
	HeartbeatMS                                  int64
	ID                                           uint64
	Text, Selection, Attachment                  string
	Scenes                                       []scene
	Overlays                                     []overlay
	Media                                        []mediaOverlay
	Documents                                    []documentRequest
	Cancelled                                    []uint64
	OpenPTY                                      *ptyInfo
}

type inputMessage struct {
	Type        string           `json:"type"`
	ID          uint64           `json:"id,omitempty"`
	Key         string           `json:"key,omitempty"`
	Text        string           `json:"text,omitempty"`
	Modifiers   uint16           `json:"modifiers,omitempty"`
	Button      int              `json:"button,omitempty"`
	X           int              `json:"x,omitempty"`
	Y           int              `json:"y,omitempty"`
	Width       int              `json:"width,omitempty"`
	Height      int              `json:"height,omitempty"`
	Focused     bool             `json:"focused,omitempty"`
	OK          bool             `json:"ok,omitempty"`
	Data        json.RawMessage  `json:"data,omitempty"`
	MediaStatus *api.MediaStatus `json:"mediaStatus,omitempty"`
}

type ptyInfo struct {
	ID, Title, State, View string
	Occupied               bool
}
