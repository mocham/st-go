package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testOutput struct{ bytes.Buffer }

func (*testOutput) Flush() error { return nil }

func writeTestCorpus(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "corpus.sqlite3")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE archive_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
		INSERT INTO archive_meta VALUES ('catalogue_title', 'Test corpus');
		INSERT INTO archive_meta VALUES ('catalogue_description', 'A static test corpus.');
		CREATE TABLE documents (
			id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, dir TEXT NOT NULL,
			name TEXT NOT NULL, extension TEXT NOT NULL, title TEXT NOT NULL,
			course TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL,
			visibility TEXT NOT NULL, sequence INTEGER NOT NULL, content TEXT NOT NULL,
			search_text TEXT NOT NULL, size INTEGER NOT NULL, mod_time_ns INTEGER NOT NULL,
			sha256 TEXT NOT NULL, indexed_at TEXT NOT NULL
		);
		INSERT INTO documents VALUES
			(1, 'AA/shared/macros.tex', 'AA/shared', 'macros.tex', '.tex', 'Macros', 'AA', 'include', 'current', 'user', 0,
			 '\newcommand{\R}{\mathbb R}', 'newcommand real', 29, 1, 'one', 'now'),
			(2, 'AA/lecture.tex', 'AA', 'lecture.tex', '.tex', 'Lecture one', 'AA', 'lecture', 'current', 'user', 1,
			 '\documentclass{article}\input{shared/macros}\begin{document}\section{First topic}Needle $\R$.\end{document}', 'Needle real', 90, 2, 'two', 'now'),
			(3, 'AA/notes.md', 'AA', 'notes.md', '.md', 'Notes', 'AA', 'note', 'historical', 'user', 2,
			 '# Notes
Markdown body with inline $x^2$.', 'markdown body inline math', 40, 3, 'three', 'now'),
			(4, 'BB/reference.tex', 'BB', 'reference.tex', '.tex', 'Reference', 'BB', 'reference', 'current', 'public', 1,
			 '\begin{document}Reference.\end{document}', 'reference', 40, 4, 'four', 'now');
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCorpusHierarchySearchAndInputExpansion(t *testing.T) {
	file := writeTestCorpus(t)
	corpus, err := openCorpus(file)
	if err != nil {
		t.Fatal(err)
	}
	defer corpus.close()
	if corpus.label != "Test corpus" || len(corpus.documents) != 4 {
		t.Fatalf("opened corpus = %q with %d documents", corpus.label, len(corpus.documents))
	}
	courses := corpus.courses()
	if len(courses) != 2 || courses[0].Name != "AA" || courses[0].Count != 3 || courses[1].Name != "BB" {
		t.Fatalf("courses = %#v", courses)
	}
	documents := corpus.documentsForCourse("AA")
	if len(documents) != 3 || documents[0].Title != "Macros" || documents[1].Title != "Lecture one" {
		t.Fatalf("AA documents = %#v", documents)
	}
	loaded, err := corpus.document(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	expanded := corpus.expandedSource(context.Background(), loaded)
	if !strings.Contains(expanded, `\newcommand{\R}{\mathbb R}`) || strings.Contains(expanded, `\input{shared/macros}`) {
		t.Fatalf("expanded source = %q", expanded)
	}
	results, err := corpus.search(context.Background(), "needle", 10)
	if err != nil || len(results) != 1 || results[0].ID != 2 {
		t.Fatalf("search results = %#v, %v", results, err)
	}
	if _, err := corpus.db.Exec(`UPDATE archive_meta SET value='changed' WHERE key='catalogue_title'`); err == nil {
		t.Fatal("immutable corpus connection accepted a write")
	}
}

func TestCorpusAddsUsedTableCompanionToStandaloneFragment(t *testing.T) {
	file := writeTestCorpus(t)
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO documents VALUES
		(5, 'CC/example-tables.tex', 'CC', 'example-tables.tex', '.tex', 'Tables', 'CC', 'include', 'current', 'user', 0,
		 '\newcommand{\ExampleTable}{\begin{tabular}{cc}Name & $x$\\A & $1$\end{tabular}}', 'table', 80, 5, 'five', 'now'),
		(6, 'CC/example.tex', 'CC', 'example.tex', '.tex', 'Example', 'CC', 'include', 'current', 'user', 1,
		 '\section[Short]{Long heading}\ExampleTable', 'example', 48, 6, 'six', 'now')`)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	opened, err := openCorpus(file)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	document, err := opened.document(context.Background(), 6)
	if err != nil {
		t.Fatal(err)
	}
	expanded := opened.expandedSource(context.Background(), document)
	if !strings.Contains(expanded, `\newcommand{\ExampleTable}`) || !strings.Contains(expanded, `\begin{document}`) {
		t.Fatalf("archive table companion was not attached: %s", expanded)
	}
}

func TestDatabaseCandidateDiscovery(t *testing.T) {
	directory := t.TempDir()
	corpus := writeTestCorpus(t)
	copyTarget := filepath.Join(directory, "nested", "archive.sqlite3")
	if err := os.MkdirAll(filepath.Dir(copyTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyTarget, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ignore.txt"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := databaseCandidates([]string{directory})
	if err != nil || len(paths) != 1 || paths[0] != copyTarget {
		t.Fatalf("database candidates = %#v, %v", paths, err)
	}
}

func TestBrowserBuildsDatabaseCourseDocumentHierarchy(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	browser := &Browser{corpora: []*corpus{opened}, visible: 20, view: viewState{kind: viewDatabases, corpusIndex: -1}}
	browser.buildEntries()
	if len(browser.entries) != 1 || browser.entries[0].kind != entryDatabase {
		t.Fatalf("database entries = %#v", browser.entries)
	}
	browser.view = viewState{kind: viewCourses, corpusIndex: 0}
	browser.idx = 0
	browser.buildEntries()
	if len(browser.entries) != 3 || browser.entries[1].kind != entryCourse || browser.entries[1].label != "AA" {
		t.Fatalf("course entries = %#v", browser.entries)
	}
	browser.view = viewState{kind: viewDocuments, corpusIndex: 0, course: "BB"}
	browser.idx = 0
	browser.buildEntries()
	if len(browser.entries) != 2 || browser.entries[1].kind != entryDocument || browser.entries[1].document.Title != "Reference" {
		t.Fatalf("document entries = %#v", browser.entries)
	}
	browser.view = viewState{kind: viewSearch, corpusIndex: -1, query: "reference"}
	browser.idx = 0
	browser.buildEntries()
	if len(browser.entries) != 2 || browser.entries[1].kind != entryDocument || browser.entries[1].corpusIndex != 0 {
		t.Fatalf("search entries = %#v", browser.entries)
	}
}

func TestPreviewRendersExpandedArchiveTeXWithoutServer(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	metadata := opened.documentPaths["AA/lecture.tex"]
	previewer := newDocumentPreviewer(t.TempDir())
	preview, err := previewer.render(context.Background(), opened, metadata, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(preview.path)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.pdf || preview.pageCount < 1 || preview.mathErrors != 0 || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("preview = %+v, prefix=%q", preview, data[:min(len(data), 8)])
	}
	if len(preview.toc) != 1 || preview.toc[0].Title != "First topic" || preview.toc[0].Page < 1 {
		t.Fatalf("preview contents = %#v", preview.toc)
	}

	// Each page-size variant is cached independently and reused exactly.
	sameSize, err := previewer.render(context.Background(), opened, metadata, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	otherSize, err := previewer.render(context.Background(), opened, metadata, 140, 160)
	if err != nil {
		t.Fatal(err)
	}
	if sameSize.path != preview.path || otherSize.path == preview.path || len(previewer.cache) != 2 {
		t.Fatalf("size-variant cache: first=%q same=%q other=%q entries=%d", preview.path, sameSize.path, otherSize.path, len(previewer.cache))
	}
}

func TestPreviewRendersArchiveMarkdownWithInlineMath(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	metadata := opened.documentPaths["AA/notes.md"]
	previewer := newDocumentPreviewer(t.TempDir())
	preview, err := previewer.render(context.Background(), opened, metadata, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(preview.path)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.pdf || preview.pageCount < 1 || preview.mathErrors != 0 || !bytes.HasPrefix(data, []byte("%PDF-")) || bytes.Contains(data, []byte("/Subtype /Image")) {
		t.Fatalf("Markdown preview = %+v, bytes=%d", preview, len(data))
	}
	if len(preview.toc) != 1 || preview.toc[0].Title != "Notes" || preview.toc[0].Page < 1 {
		t.Fatalf("Markdown contents = %#v", preview.toc)
	}
}

func TestCorpusRefreshRemovesStalePreviewRevision(t *testing.T) {
	file := writeTestCorpus(t)
	opened, err := openCorpus(file)
	if err != nil {
		t.Fatal(err)
	}
	previewer := newDocumentPreviewer(t.TempDir())
	browser := &Browser{corpora: []*corpus{opened}, previewer: previewer}
	metadata := opened.documentPaths["AA/lecture.tex"]
	preview, err := previewer.render(context.Background(), opened, metadata, 120, 160)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, updateErr := db.Exec(`UPDATE documents
		SET content='\newcommand{\R}{\mathbb Q}', sha256='changed-include', mod_time_ns=99
		WHERE id=1`)
	closeErr := db.Close()
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := browser.refreshCorpora(); err != nil {
		t.Fatal(err)
	}
	defer browser.corpora[0].close()
	if len(previewer.cache) != 0 {
		t.Fatalf("stale preview cache entries = %d, want 0", len(previewer.cache))
	}
	if _, err := os.Stat(preview.path); !os.IsNotExist(err) {
		t.Fatalf("stale preview %q still exists: %v", preview.path, err)
	}
	refreshed := browser.corpora[0]
	updated, err := previewer.render(context.Background(), refreshed, refreshed.documentPaths["AA/lecture.tex"], 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if updated.path == preview.path || len(previewer.cache) != 1 {
		t.Fatalf("updated preview = %q, old = %q, entries=%d", updated.path, preview.path, len(previewer.cache))
	}
}

func TestDocumentPreviewCacheBoundsAllRecords(t *testing.T) {
	dir := t.TempDir()
	previewer := newDocumentPreviewer(dir)
	var paths []string
	for index := 0; index < maxDocumentPreviews+2; index++ {
		sourceID := previewSourceID{corpus: "corpus.sqlite3", id: int64(index + 1)}
		previewer.prepareSource(sourceID, "revision")
		path := filepath.Join(dir, fmt.Sprintf("document-%d.pdf", index))
		if err := os.WriteFile(path, []byte("pdf"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		previewer.storeSourcePreview(sourceID, fmt.Sprintf("key-%d", index), renderedPreview{path: path, pdf: true})
	}
	if len(previewer.cache) != maxDocumentPreviews || len(previewer.order) != maxDocumentPreviews {
		t.Fatalf("global preview cache = %d entries / %d order, want %d", len(previewer.cache), len(previewer.order), maxDocumentPreviews)
	}
	for _, stale := range paths[:2] {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("globally evicted preview %q still exists: %v", stale, err)
		}
	}
}

func TestDocumentContentsUsePinnedOpenedPDF(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	metadata := opened.documentPaths["AA/lecture.tex"]
	previewer := newDocumentPreviewer(t.TempDir())
	preview, err := previewer.render(context.Background(), opened, metadata, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	browser := &Browser{
		corpora: []*corpus{opened}, previewer: previewer,
		visible: 20, view: viewState{kind: viewTOC, corpusIndex: 0, course: "AA", documentID: metadata.ID},
		openedDocumentID: metadata.ID, openedPreview: preview,
	}
	browser.buildEntries()
	if len(browser.entries) != 3 || browser.entries[1].kind != entryTOC || browser.entries[2].toc.Title != "First topic" {
		t.Fatalf("contents entries = %#v", browser.entries)
	}
	if len(previewer.cache) != 1 {
		t.Fatalf("building contents regenerated PDF: %d cache entries", len(previewer.cache))
	}
	// A resize redraw only changes display geometry; it does not request a new
	// latexpdf size variant for the open document.
	browser.rows, browser.cols, browser.previewW = 50, 160, 160
	browser.buildEntries()
	if browser.openedPreview.path != preview.path || len(previewer.cache) != 1 {
		t.Fatalf("open PDF changed during resize: path=%q entries=%d", browser.openedPreview.path, len(previewer.cache))
	}
}

func TestDocumentSelectionPreviewsAndEnterShowsContents(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	output := &testOutput{}
	browser := &Browser{fd: -1, out: output, corpora: []*corpus{opened}}
	if err := browser.setup(); err != nil {
		opened.close()
		t.Fatal(err)
	}
	defer browser.cleanup()
	if !browser.navigation.Visible || !browser.navigation.Floating() {
		t.Fatal("navigation is hidden at startup")
	}
	browser.terminalSize()
	browser.view = viewState{kind: viewDocuments, corpusIndex: 0, course: "AA"}
	browser.idx = 1
	browser.buildEntries()
	browser.selectIndex(2) // Lecture one, including a real rendered section.
	if len(browser.previewer.cache) != 1 || browser.view.kind != viewDocuments || !browser.navigation.Visible || browser.pages < 1 {
		t.Fatalf("selected preview: view=%d nav=%v cache=%d pages=%d", browser.view.kind, browser.navigation, len(browser.previewer.cache), browser.pages)
	}
	if !strings.Contains(output.String(), "fit-height page 1\x1b\\") {
		t.Fatal("selection did not emit a fit-height PDF preview")
	}
	selectedPath, err := os.Readlink(browser.previewLink)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	browser.enterSelected()
	if browser.view.kind != viewTOC || !browser.navigation.Floating() || len(browser.previewer.cache) != 1 {
		t.Fatalf("open state: view=%d nav=%v cache=%d", browser.view.kind, browser.navigation, len(browser.previewer.cache))
	}
	openedPath := browser.openedPreview.path
	if openedPath != selectedPath || !strings.Contains(output.String(), "First topic") {
		t.Fatalf("Enter did not reuse the preview and display its TOC: selected=%q opened=%q", selectedPath, openedPath)
	}
	if len(browser.entries) != 3 || browser.entries[2].kind != entryTOC {
		t.Fatalf("contents entries = %#v", browser.entries)
	}
	output.Reset()
	browser.selectIndex(2)
	if browser.page != browser.entries[2].toc.Page || !strings.Contains(output.String(), fmt.Sprintf("fit-height page %d\x1b\\", browser.page)) {
		t.Fatal("selecting a TOC section did not jump to its rendered PDF page")
	}
	browser.renderAll() // same path is redrawn after a resize/event, not regenerated
	if browser.openedPreview.path != openedPath || len(browser.previewer.cache) != 1 {
		t.Fatalf("redraw regenerated PDF: path=%q cache=%d", browser.openedPreview.path, len(browser.previewer.cache))
	}
	browser.toggleNavigation()
	if browser.navigation.Visible {
		t.Fatal("Tab did not hide navigation")
	}
	browser.toggleNavigation()
	if !browser.navigation.Visible || browser.view.kind != viewTOC {
		t.Fatal("second Tab did not restore the table of contents")
	}
}
