package web

import (
	"encoding/json"
	"fmt"
	"st-go/pdfmeta"
	"st-go/platform_neutral/api"
)

type documentRequest struct {
	ID    uint64            `json:"id"`
	Media uint64            `json:"media"`
	Query api.DocumentQuery `json:"query"`
}
type documentJob struct {
	request documentRequest
	client  *client
	done    func([]byte, error)
}

func (d *Driver) QueryMedia(id uint64, q api.DocumentQuery, done func([]byte, error)) func() {
	d.mu.Lock()
	if d.media[id] == nil || len(d.documents) >= 32 {
		d.mu.Unlock()
		go done(nil, fmt.Errorf("document query unavailable"))
		return func() {}
	}
	if d.documents == nil {
		d.documents = make(map[uint64]*documentJob)
	}
	d.nextDocumentID++
	key := d.nextDocumentID
	d.documents[key] = &documentJob{request: documentRequest{key, id, q}, done: done}
	d.mu.Unlock()
	d.notify()
	return func() {
		d.mu.Lock()
		if job := d.documents[key]; job != nil && job.client != nil && job.client == d.client {
			if d.documentCancels == nil {
				d.documentCancels = make(map[*client][]uint64)
			}
			d.documentCancels[job.client] = append(d.documentCancels[job.client], key)
		}
		delete(d.documents, key)
		d.mu.Unlock()
		d.notify()
	}
}
func (d *Driver) documentSnapshot(c *client) *serverMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := &serverMessage{Type: "document"}
	m.Cancelled = d.documentCancels[c]
	delete(d.documentCancels, c)
	for _, job := range d.documents {
		if job.client != c {
			job.client = c
			m.Documents = append(m.Documents, job.request)
		}
	}
	if len(m.Documents) == 0 && len(m.Cancelled) == 0 {
		return nil
	}
	return m
}
func (d *Driver) documentReply(c *client, id uint64, data json.RawMessage) error {
	if len(data) > pdfmeta.MaxBytes || !json.Valid(data) {
		return fmt.Errorf("invalid document response")
	}
	d.mu.Lock()
	job := d.documents[id]
	if job != nil && job.client == c {
		delete(d.documents, id)
	} else {
		job = nil
	}
	d.mu.Unlock()
	if job != nil {
		job.done(data, nil)
	}
	return nil
}
