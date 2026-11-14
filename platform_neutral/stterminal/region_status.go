package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"st-go/limits"
)

// RegionStatus is a retained footer independent of the region's PDF/HTML/list.
// Text and colors are application-owned; the terminal knows nothing about UUIDs.
type RegionStatus struct {
	Items []RegionStatusItem `json:"items"`
}

type RegionStatusItem struct {
	Text  string `json:"text"`
	Color uint32 `json:"color"`
}

type RegionStatusHooks interface {
	SetRegionStatus(RegionID, RegionStatus) error
}

func (s RegionStatus) Validate() error {
	if len(s.Items) > 256 {
		return fmt.Errorf("too many region status items")
	}
	for _, item := range s.Items {
		if !listText(item.Text, 128) || item.Color>>24 != 255 {
			return fmt.Errorf("invalid region status item")
		}
	}
	return nil
}

func (t *Term) dslRegionStatus(id RegionID, args []string) error {
	hooks, ok := t.hooks.(RegionStatusHooks)
	if !ok || len(args) != 1 {
		return fmt.Errorf("region status requires supported base64 JSON or clear")
	}
	var status RegionStatus
	if args[0] != "clear" {
		if len(args[0]) > limits.ControlBytes-128 {
			return fmt.Errorf("region status exceeds resource limit")
		}
		data, err := base64.StdEncoding.DecodeString(args[0])
		if err != nil {
			return fmt.Errorf("invalid region status encoding")
		}
		if err := json.Unmarshal(data, &status); err != nil {
			return fmt.Errorf("invalid region status JSON")
		}
		if err := status.Validate(); err != nil {
			return err
		}
	}
	return hooks.SetRegionStatus(id, status)
}
