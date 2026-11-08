package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

type renderResult struct {
	hash [sha256.Size]byte
	pdf  []byte
	err  error
}

type refreshClock struct {
	gap                        time.Duration
	lastAttempt, lastPublished time.Time
	attempted                  [sha256.Size]byte
	published                  [sha256.Size]byte
	hasAttempt                 bool
	hasPublished               bool
	busy                       bool
}

func (c *refreshClock) ready(now time.Time, hash [sha256.Size]byte) bool {
	return !c.busy && (!c.hasAttempt || hash != c.attempted) && (!c.hasPublished || hash != c.published) &&
		(now.Sub(c.lastAttempt) >= c.gap) && (now.Sub(c.lastPublished) >= c.gap)
}

func (c *refreshClock) start(now time.Time, hash [sha256.Size]byte) {
	c.lastAttempt, c.attempted, c.hasAttempt, c.busy = now, hash, true, true
}

func renderSource(file string, source *sourceSnapshot) renderResult {
	result := renderResult{hash: source.hash}
	title := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if value, ok := latexpdf.FindCommandArgument(source.text, "title"); ok {
		title = latexpdf.PlainText(value)
	}
	author, _ := latexpdf.FindCommandArgument(source.text, "author")
	// A fresh renderer per revision releases whole-document/formula caches;
	// keeping one Renderer forever would retain every saved revision.
	rendered, err := latexpdf.NewRenderer().RenderPDFResultWithOptions(latexpdf.Document{
		Source: source.text, Title: title, Author: latexpdf.PlainText(author),
	}, latexpdf.PDFOptions{PageWidth: 140, PageHeight: 297, Compact: true, FontScale: 0.8})
	if err == nil && !limits.Check("vimtex PDF", int64(len(rendered.PDF)), limits.FileBytes) {
		err = fmt.Errorf("rendered PDF exceeds resource limit")
	}
	if rendered.MathErrors > 0 {
		limits.Report("vimtex", "%d mathematical expressions could not be rendered", rendered.MathErrors)
	}
	result.pdf, result.err = rendered.PDF, err
	return result
}

func publishPDF(path string, pdf []byte) error {
	if !limits.Check("vimtex PDF", int64(len(pdf)), limits.FileBytes) {
		return fmt.Errorf("PDF exceeds resource limit")
	}
	next := path + ".next"
	if err := os.WriteFile(next, pdf, 0600); err != nil {
		return err
	}
	// A decoder already reading the old inode can finish; later requests see
	// a complete new PDF. Disk usage stays at one current + one temporary PDF.
	return os.Rename(next, path)
}
