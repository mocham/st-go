package preview

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

func TestHTMLTransferRoundTripAcrossControlLimit(t *testing.T) {
	for _, count := range []int{1, limits.ControlBytes / 10} {
		document := api.HTMLDocument{HTML: strings.Repeat("<p>世界;\x1b\\</p>\n", count), CSS: "p{color:red}"}
		data, err := EncodeHTML(document)
		if err != nil {
			t.Fatal(err)
		}
		var received []byte
		declared, committed := 0, false
		err = SendHTML(data, func(command string) error {
			if len(command) > limits.ControlBytes {
				t.Fatal("transfer emitted an oversized control string")
			}
			args := strings.Fields(strings.TrimPrefix(command, "region secondary html "))
			switch args[0] {
			case "begin":
				declared, err = strconv.Atoi(args[1])
				if err != nil || declared != len(data) {
					t.Fatal("wrong declared document length")
				}
			case "chunk":
				offset, err := strconv.Atoi(args[1])
				if err != nil || offset != len(received) {
					t.Fatal("incorrect chunk offset")
				}
				chunk, err := base64.StdEncoding.DecodeString(args[2])
				if err != nil {
					t.Fatal(err)
				}
				received = append(received, chunk...)
			case "commit":
				if declared == 0 || len(received) != declared {
					t.Fatal("committed incomplete document")
				}
				committed = true
			default:
				received, err = base64.StdEncoding.DecodeString(args[0])
				if err != nil || count != 1 {
					t.Fatal("unexpected single-command document", err)
				}
				committed = true
			}
			return nil
		})
		var got api.HTMLDocument
		if err != nil || !committed || !bytes.Equal(received, data) || json.Unmarshal(received, &got) != nil || got != document {
			t.Fatal("HTML/CSS changed across the transfer", err)
		}
	}
}

func TestHTMLTransferStopsAfterWriteFailure(t *testing.T) {
	data, err := EncodeHTML(api.HTMLDocument{HTML: strings.Repeat("a", limits.ControlBytes)})
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("closed PTY")
	calls := 0
	err = SendHTML(data, func(string) error {
		calls++
		if calls == 3 {
			return failed
		}
		return nil
	})
	if !errors.Is(err, failed) || calls != 3 {
		t.Fatal("failed transfer continued or committed", err)
	}
}
