package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

type RegionHTMLHooks interface {
	SetRegionHTML(RegionID, api.HTMLDocument) error
}

// At most one incomplete document per terminal. A new begin, replacement,
// clear, or reset releases it. The visible document changes only on commit.
type regionHTMLTransfer struct {
	id   RegionID
	size int
	data []byte
}

// Base64 keeps HTML/CSS quotes, semicolons and control characters out of the
// DCS statement grammar. Unsupported frontends silently discard this extension.
func (t *Term) dslRegionHTML(id RegionID, args []string) (err error) {
	hooks, ok := t.hooks.(RegionHTMLHooks)
	if !ok {
		return nil
	}
	if support, ok := t.hooks.(api.HTMLSupport); ok && !support.SupportsHTML() {
		return nil
	}
	defer func() {
		if err != nil {
			t.htmlTransfer = nil
		}
	}()
	if len(args) == 0 || (id != MainRegion && id != SecondaryRegion) {
		return fmt.Errorf("invalid region html command")
	}
	switch args[0] {
	case "begin":
		t.htmlTransfer = nil
		if len(args) != 2 {
			return fmt.Errorf("region html begin requires a byte count")
		}
		size, err := strconv.Atoi(args[1])
		if err != nil || size < 1 || size > limits.FileBytes {
			return fmt.Errorf("invalid region html document size")
		}
		t.htmlTransfer = &regionHTMLTransfer{id: id, size: size}
		return nil
	case "chunk":
		transfer := t.htmlTransfer
		if len(args) != 3 || len(args[2]) > limits.ControlBytes-128 || transfer == nil || transfer.id != id {
			return fmt.Errorf("unexpected region html chunk")
		}
		offset, err := strconv.Atoi(args[1])
		if err != nil || offset != len(transfer.data) {
			return fmt.Errorf("out-of-order region html chunk")
		}
		data, err := base64.StdEncoding.DecodeString(args[2])
		if err != nil || len(data) == 0 || len(data) > transfer.size-offset {
			return fmt.Errorf("invalid region html chunk")
		}
		// Allocate only after receiving real data, and never beyond the declared
		// budget (append's usual growth can otherwise overshoot that budget).
		if transfer.data == nil {
			transfer.data = make([]byte, 0, transfer.size)
		}
		transfer.data = append(transfer.data, data...)
		return nil
	case "commit":
		transfer := t.htmlTransfer
		t.htmlTransfer = nil
		if len(args) != 1 || transfer == nil || transfer.id != id || len(transfer.data) != transfer.size {
			return fmt.Errorf("incomplete region html document")
		}
		return publishRegionHTML(hooks, id, transfer.data)
	}
	t.htmlTransfer = nil
	if len(args) != 1 || len(args[0]) > limits.ControlBytes-128 {
		return fmt.Errorf("region html requires bounded base64 JSON")
	}
	data, err := base64.StdEncoding.DecodeString(args[0])
	if err != nil {
		return fmt.Errorf("invalid region html encoding")
	}
	return publishRegionHTML(hooks, id, data)
}

func publishRegionHTML(hooks RegionHTMLHooks, id RegionID, data []byte) error {
	var document api.HTMLDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("invalid region html document: %w", err)
	}
	return hooks.SetRegionHTML(id, document)
}
