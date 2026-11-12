package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"st-go/platform_neutral/api"
)

type localQuery struct {
	request  documentRequest
	token    int
	path     string
	data     []byte
	deadline time.Time
}

func (a *app) documents(m serverMessage) error {
	for _, id := range m.Cancelled {
		if a.query != nil && a.query.request.ID == id {
			if a.query.request.Query.Kind == "search" {
				if err := dcs(a.out, "pdfsearch clear"); err != nil {
					return err
				}
			}
			a.query = nil
		}
		live := a.queries[:0]
		for _, q := range a.queries {
			if q.ID != id {
				live = append(live, q)
			}
		}
		a.queries = live
	}
	for _, q := range m.Documents {
		if q.ID == 0 || q.Media == 0 || len(a.queries) >= 32 || (q.Query.Kind != "search" && q.Query.Kind != "toc" && q.Query.Kind != "text") || q.Query.Page < 0 || q.Query.Page > 100000 || len(q.Query.Text) > 4096 || strings.IndexFunc(q.Query.Text, unicode.IsControl) >= 0 {
			if err := a.send(inputMessage{Type: "document_reply", ID: q.ID, Data: errorJSON(fmt.Errorf("invalid or excessive document query"))}); err != nil {
				return err
			}
			continue
		}
		duplicate := a.query != nil && a.query.request.ID == q.ID
		for _, queued := range a.queries {
			duplicate = duplicate || queued.ID == q.ID
		}
		if !duplicate {
			q.deadline = time.Now().Add(30 * time.Second)
			a.queries = append(a.queries, q)
		}
	}
	return nil
}

func (a *app) startQuery() error {
	if a.query != nil || len(a.queries) == 0 {
		return nil
	}
	q := a.queries[0]
	key := a.mediaJob(q.Media).key
	if a.failed[key] {
		a.queries = a.queries[1:]
		return a.send(inputMessage{Type: "document_reply", ID: q.ID, Data: errorJSON(fmt.Errorf("document resource unavailable"))})
	}
	path := a.resources[key]
	if path == "" {
		return nil
	}
	if q.Query.Kind != "search" {
		if a.preview == nil || a.preview.media.ID != q.Media {
			a.queries = a.queries[1:]
			return a.send(inputMessage{Type: "document_reply", ID: q.ID, Data: errorJSON(fmt.Errorf("metadata requires the visible PDF preview"))})
		}
		if !a.preview.ready {
			return nil
		}
	}
	a.queries = a.queries[1:]
	a.queryToken = a.queryToken%(1<<30) + 1
	a.query = &localQuery{request: q, token: a.queryToken, path: path, deadline: time.Now().Add(15 * time.Second)}
	if !strings.HasSuffix(path, ".pdf") {
		return a.finishQuery(nil, fmt.Errorf("not a PDF document"))
	}
	return a.queryChunk()
}

func (a *app) queryChunk() error {
	q := a.query
	if q == nil {
		return nil
	}
	var command string
	switch q.request.Query.Kind {
	case "search":
		command = fmt.Sprintf("pdfsearch chunk %d %d", q.token, len(q.data))
		if len(q.data) == 0 {
			command = fmt.Sprintf("pdfsearch query %d %s %s", q.token, quote(q.path), quote(q.request.Query.Text))
		}
	case "toc":
		command = fmt.Sprintf("region secondary toc %d %d", q.token, len(q.data))
	case "text":
		command = fmt.Sprintf("region secondary text %d %d %d", q.token, max(1, q.request.Query.Page), len(q.data))
	}
	q.deadline = time.Now().Add(15 * time.Second)
	return dcs(a.out, command)
}

func (a *app) queryReply(text string) error {
	q := a.query
	if q == nil {
		return nil
	}
	f := strings.Fields(text)
	var token, offset, next int
	var data string
	if len(f) == 6 && f[0] == "pdfsearch" && f[1] == "data" && q.request.Query.Kind == "search" {
		var e1, e2, e3 error
		token, e1 = strconv.Atoi(f[2])
		offset, e2 = strconv.Atoi(f[3])
		next, e3 = strconv.Atoi(f[4])
		if e1 != nil || e2 != nil || e3 != nil {
			return nil
		}
		data = f[5]
	} else if len(f) == 9 && strings.Join(f[:3], " ") == "region secondary data" && q.request.Query.Kind == f[4] {
		var e1, e2, e3, e4 error
		var page int
		token, e1 = strconv.Atoi(f[3])
		page, e2 = strconv.Atoi(f[5])
		offset, e3 = strconv.Atoi(f[6])
		next, e4 = strconv.Atoi(f[7])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			return nil
		}
		if q.request.Query.Kind == "text" && page != max(1, q.request.Query.Page) {
			return nil
		}
		data = f[8]
	} else {
		return nil
	}
	if token != q.token {
		return nil
	}
	chunk, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(chunk) > 3072 || len(q.data)+len(chunk) > 1<<20 || offset != len(q.data) || (next != -1 && (next != offset+len(chunk) || len(chunk) == 0)) {
		return a.finishQuery(nil, fmt.Errorf("invalid local PDF metadata chunk"))
	}
	q.data = append(q.data, chunk...)
	if next != -1 {
		return a.queryChunk()
	}
	if !json.Valid(q.data) {
		return a.finishQuery(nil, fmt.Errorf("invalid local PDF metadata JSON"))
	}
	return a.finishQuery(q.data, nil)
}

func (a *app) finishQuery(data json.RawMessage, err error) error {
	q := a.query
	if q == nil {
		return nil
	}
	a.query = nil
	if err != nil {
		data = errorJSON(err)
	}
	return a.send(inputMessage{Type: "document_reply", ID: q.request.ID, Data: data})
}

func (a *app) resourceFailure(key string, err error) error {
	live := a.queries[:0]
	for _, q := range a.queries {
		if a.mediaJob(q.Media).key == key {
			if sendErr := a.send(inputMessage{Type: "document_reply", ID: q.ID, Data: errorJSON(err)}); sendErr != nil {
				return sendErr
			}
		} else {
			live = append(live, q)
		}
	}
	a.queries = live
	if p := a.preview; p != nil && p.job.key == key && p.media.ID != 0 {
		return a.send(inputMessage{Type: "media_status", MediaStatus: &api.MediaStatus{ID: p.media.ID, Generation: p.media.Generation, PositionID: p.media.PositionID, Error: err.Error()}})
	}
	return nil
}
