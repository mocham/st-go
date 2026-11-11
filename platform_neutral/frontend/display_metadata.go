package frontend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type regionMetadataJob struct {
	query    term.RegionDocumentQuery
	revision uint64
	path     string
}

type regionMetadataReply struct {
	revision uint64
	text     string
}
type regionMetadataCache struct {
	revision uint64
	kind     string
	page     int
	data     []byte
}

func (t *Terminal) QueryRegionDocument(id term.RegionID, query term.RegionDocumentQuery) error {
	s := &t.displayRegions
	s.mu.Lock()
	defer s.mu.Unlock()
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if surface.media.ID != 0 {
		return t.querySceneDocument(surface, query)
	}
	if surface.request == nil || !surface.request.Continuous || s.metadataJobs == nil {
		return fmt.Errorf("PDF metadata requires a continuous PDF view")
	}
	if query.Token < 1 || query.Token > 1<<30 || query.Offset < 0 || query.Offset > pdfmeta.MaxBytes ||
		(query.Kind != "toc" && query.Kind != "text") || (query.Kind == "text" && (query.Page < 1 || query.Page > limits.PreviewPages)) {
		return fmt.Errorf("invalid PDF metadata request")
	}
	select {
	case <-s.metadataJobs:
	default:
	}
	s.metadataJobs <- regionMetadataJob{query, s.documentRevision, surface.request.Path}
	return nil
}

// Browser metadata uses the same chunked DCS response as native PDF workers.
// A newer document/query invalidates the completion before it can reach the PTY.
func (t *Terminal) querySceneDocument(surface *regionSurface, q term.RegionDocumentQuery) error {
	if q.Token < 1 || q.Offset < 0 || q.Offset > pdfmeta.MaxBytes || (q.Kind != "toc" && q.Kind != "text") {
		return fmt.Errorf("invalid document query")
	}
	s := &t.displayRegions
	if s.documentCancel != nil {
		s.documentCancel()
	}
	s.documentQuery++
	seq, revision := s.documentQuery, s.documentRevision
	s.documentCancel = t.host.(api.DocumentHost).QueryMedia(surface.media.ID, api.DocumentQuery{Kind: q.Kind, Page: q.Page}, func(data []byte, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if t.closed() || revision != s.documentRevision || seq != s.documentQuery {
			return
		}
		if err != nil {
			data, _ = json.Marshal(pdfmeta.Payload{Error: err.Error()})
		}
		if q.Offset > len(data) {
			return
		}
		end := min(len(data), q.Offset+pdfmeta.ChunkBytes)
		next := end
		if end == len(data) {
			next = -1
		}
		text := fmt.Sprintf("\x1bPregion secondary data %d %s %d %d %d %s\x1b\\", q.Token, q.Kind, q.Page, q.Offset, next, base64.StdEncoding.EncodeToString(data[q.Offset:end]))
		if s.metadataReplies == nil {
			s.metadataReplies = make(chan regionMetadataReply, 1)
		}
		select {
		case <-s.metadataReplies:
		default:
		}
		s.metadataReplies <- regionMetadataReply{revision, text}
	})
	return nil
}

// This runs only in the native document's existing decoder goroutine. Metadata
// requests have their own mailbox, so scroll prefetch cannot silently drop them.
func (t *Terminal) processRegionMetadata(job regionMetadataJob, document **scrollPDF, revision *uint64, cache *regionMetadataCache) {
	s := &t.displayRegions
	s.mu.Lock()
	current := !t.closed() && s.secondary != nil && job.revision == s.documentRevision
	s.mu.Unlock()
	if !current {
		return
	}
	if cache.revision != job.revision || cache.kind != job.query.Kind || cache.page != job.query.Page || cache.data == nil {
		var err error
		if *document == nil || *revision != job.revision {
			var data []byte
			data, err = limits.ReadFile(job.path)
			if err == nil {
				var next *scrollPDF
				next, err = openScrollPDF(data)
				if err == nil {
					(*document).close()
					*document, *revision = next, job.revision
				}
			}
		}
		var result pdfmeta.Payload
		if err == nil {
			result, err = (*document).metadata(job.query.Kind, job.query.Page)
		}
		if err != nil {
			result = pdfmeta.Payload{Error: err.Error()}
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > pdfmeta.MaxBytes {
			data, _ = json.Marshal(pdfmeta.Payload{Error: "PDF metadata exceeds the preview budget"})
		}
		*cache = regionMetadataCache{job.revision, job.query.Kind, job.query.Page, data}
	}
	offset := job.query.Offset
	if offset > len(cache.data) {
		return
	}
	end := min(len(cache.data), offset+pdfmeta.ChunkBytes)
	next := end
	if end == len(cache.data) {
		next = -1
	}
	frame := fmt.Sprintf("\x1bPregion secondary data %d %s %d %d %d %s\x1b\\", job.query.Token, job.query.Kind, job.query.Page, offset, next,
		base64.StdEncoding.EncodeToString(cache.data[offset:end]))
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.closed() || s.secondary == nil || job.revision != s.documentRevision {
		return
	}
	if s.metadataReplies == nil {
		s.metadataReplies = make(chan regionMetadataReply, 1)
	}
	select {
	case <-s.metadataReplies:
	default:
	}
	s.metadataReplies <- regionMetadataReply{job.revision, frame}
}
