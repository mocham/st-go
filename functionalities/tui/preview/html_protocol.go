package preview

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

// EncodeHTML prepares a reusable document for SendHTML. The document budget is
// independent of the size limit on each terminal control string.
func EncodeHTML(document api.HTMLDocument) ([]byte, error) {
	if len(document.HTML) > limits.FileBytes-len(document.CSS) {
		return nil, fmt.Errorf("HTML preview exceeds %d MiB document limit", limits.FileBytes>>20)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	// This is a JSON protocol payload, not a script embedded in an HTML page.
	// Escaping every angle bracket needlessly multiplies SVG/HTML transfer size.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if data.Len() > limits.FileBytes {
		return nil, fmt.Errorf("HTML preview exceeds %d MiB document limit", limits.FileBytes>>20)
	}
	return data.Bytes(), nil
}

// SendHTML emits independently framed commands through the application's usual
// DCS writer. Small documents keep the original single-command representation;
// larger ones are assembled off-screen and published only after commit.
func SendHTML(data []byte, command func(string) error) error {
	if len(data) == 0 || len(data) > limits.FileBytes {
		return fmt.Errorf("invalid HTML document size")
	}
	const prefix = "region secondary html "
	if base64.StdEncoding.EncodedLen(len(data)) <= limits.ControlBytes-128 {
		return command(prefix + base64.StdEncoding.EncodeToString(data))
	}
	if err := command(fmt.Sprintf("%sbegin %d", prefix, len(data))); err != nil {
		return err
	}
	const chunkBytes = 48 << 10 // 64 KiB base64; well below the DCS limit.
	for offset := 0; offset < len(data); offset += chunkBytes {
		chunk := data[offset:min(offset+chunkBytes, len(data))]
		if err := command(fmt.Sprintf("%schunk %d %s", prefix, offset, base64.StdEncoding.EncodeToString(chunk))); err != nil {
			return err
		}
	}
	return command(prefix + "commit")
}
