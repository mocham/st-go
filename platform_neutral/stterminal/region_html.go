package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

type RegionHTMLHooks interface {
	SetRegionHTML(RegionID, api.HTMLDocument) error
}

// Base64 keeps HTML/CSS quotes, semicolons and control characters out of the
// DCS statement grammar. Unsupported frontends silently discard this extension.
func (t *Term) dslRegionHTML(id RegionID, args []string) error {
	hooks, ok := t.hooks.(RegionHTMLHooks)
	if !ok {
		return nil
	}
	if len(args) != 1 || len(args[0]) > limits.ControlBytes-128 {
		return fmt.Errorf("region html requires bounded base64 JSON")
	}
	data, err := base64.StdEncoding.DecodeString(args[0])
	if err != nil {
		return fmt.Errorf("invalid region html encoding")
	}
	var document api.HTMLDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("invalid region html document: %w", err)
	}
	return hooks.SetRegionHTML(id, document)
}
