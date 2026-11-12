package appbus

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestChunkReassemblyInterleavingAndMissingChunk(t *testing.T) {
	c, err := New(io.Discard, ID(), "test")
	if err != nil {
		t.Fatal(err)
	}
	sender, message := ID(), ID()
	data, _ := json.Marshal(strings.Repeat("多", 4000))
	count := (len(data) + ChunkBytes - 1) / ChunkBytes
	frame := func(i int) string {
		return fmt.Sprintf("app-bus %s message %s %d test %s %d %d %s", c.ID, sender, i+1, message, i, count, base64.RawStdEncoding.EncodeToString(data[i*ChunkBytes:min(len(data), (i+1)*ChunkBytes)]))
	}
	for i := 0; i < count; i++ {
		got, err := c.Receive(frame(i))
		if err != nil {
			t.Fatal(err)
		}
		if i < count-1 && got != nil {
			t.Fatal("partial message delivered")
		}
		if i == count-1 && (got == nil || string(got.Data) != string(data)) {
			t.Fatal("reassembly corrupted")
		}
	}
	if _, err := c.Receive(frame(0)); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Receive(frame(2)); got != nil || err == nil {
		t.Fatal("missing chunk wasn't rejected")
	}
}
