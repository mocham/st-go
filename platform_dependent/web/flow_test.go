package web

import (
	"encoding/json"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestSlowBrowserCoalescesDamageAndStillAcceptsInput(t *testing.T) {
	d := testDriver(t)
	input := make(chan string, 4)
	d.callbacks.Text = func(s string) { input <- s }
	runDriver(t, d)
	conn := dial(t, d)
	send := func(m inputMessage) {
		t.Helper()
		if err := websocket.JSON.Send(conn, m); err != nil {
			t.Fatal(err)
		}
	}
	readBatch := func() uint64 {
		t.Helper()
		for {
			var data []byte
			if err := websocket.Message.Receive(conn, &data); err != nil {
				t.Fatal(err)
			}
			var m serverMessage
			if len(data) > 0 && data[0] == '{' && json.Unmarshal(data, &m) == nil && m.Type == "paint" {
				return m.ID
			}
		}
	}
	first := readBatch()
	send(inputMessage{Type: "paint_ack", ID: first})
	// Wait for the reader's ordered input barrier before generating damage.
	send(inputMessage{Type: "text", Text: "ready"})
	<-input
	pixels := make([]uint32, d.width*d.height)
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	pending := readBatch()
	for i := range 100 {
		for j := range pixels {
			pixels[j] = 0xff000000 | uint32(i)
		}
		d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	}
	send(inputMessage{Type: "text", Text: "responsive"})
	select {
	case got := <-input:
		if got != "responsive" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("slow browser blocked input")
	}
	// A control reply is a barrier proving the writer is active, but it must
	// not send any new pixels until the outstanding paint has been acknowledged.
	d.mu.Lock()
	c := d.client
	d.mu.Unlock()
	c.controls <- serverMessage{Type: "barrier"}
	var reply serverMessage
	if err := websocket.JSON.Receive(conn, &reply); err != nil || reply.Type != "barrier" {
		t.Fatalf("writer queued pixels behind a slow browser: %+v, %v", reply, err)
	}
	send(inputMessage{Type: "paint_ack", ID: pending})
	if next := readBatch(); next != pending+1 {
		t.Fatal("pending updates were not coalesced into one presentation")
	}
}
