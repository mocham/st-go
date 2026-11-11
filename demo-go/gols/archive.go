package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mocham/GoLS/latexpdf"
	_ "modernc.org/sqlite"
)

type documentMeta struct {
	ID         int64
	Path       string
	Name       string
	Extension  string
	Title      string
	Course     string
	Kind       string
	Status     string
	Visibility string
	Sequence   int
	Size       int64
	ModTimeNS  int64
	SHA256     string
}

type documentRecord struct {
	documentMeta
	Content string
}

type courseSummary struct {
	Name  string
	Count int
}

type corpus struct {
	file          string
	revision      string
	label         string
	description   string
	generated     string
	db            *sql.DB
	documents     []documentMeta
	documentPaths map[string]documentMeta
	hasVisibility bool
}

func openCorpus(file string) (*corpus, error) {
	absolute, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	info, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = info
	}
	dsn := url.URL{Scheme: "file", Path: absolute}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	result := &corpus{file: absolute, db: db, documentPaths: make(map[string]documentMeta)}
	if err := result.load(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return result, nil
}

func (corpus *corpus) load(ctx context.Context) error {
	var documentsTable int
	if err := corpus.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='documents'`).Scan(&documentsTable); err != nil {
		return err
	}
	if documentsTable != 1 {
		return errors.New("not a GoLS corpus database")
	}
	columns, err := corpus.db.QueryContext(ctx, `PRAGMA table_info(documents)`)
	if err != nil {
		return err
	}
	required := map[string]bool{"id": false, "path": false, "content": false, "title": false}
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			return err
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
		if name == "visibility" {
			corpus.hasVisibility = true
		}
	}
	if err := columns.Close(); err != nil {
		return err
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("documents table is missing %s", name)
		}
	}

	metadata := map[string]string{}
	if rows, err := corpus.db.QueryContext(ctx, `SELECT key, value FROM archive_meta`); err == nil {
		for rows.Next() {
			var key, value string
			if rows.Scan(&key, &value) == nil {
				metadata[key] = value
			}
		}
		rows.Close()
	}
	corpus.label = strings.TrimSpace(metadata["catalogue_title"])
	if corpus.label == "" {
		corpus.label = strings.TrimSuffix(filepath.Base(corpus.file), filepath.Ext(corpus.file))
	}
	corpus.description = strings.TrimSpace(metadata["catalogue_description"])
	corpus.generated = strings.TrimSpace(metadata["generated_at"])

	visibility := `'user'`
	if corpus.hasVisibility {
		visibility = "visibility"
	}
	rows, err := corpus.db.QueryContext(ctx, `
		SELECT id, path, name, extension, title, course, kind, status, `+visibility+`,
		       sequence, size, mod_time_ns, sha256
		FROM documents ORDER BY path`)
	if err != nil {
		return err
	}
	defer rows.Close()
	corpus.documents = nil
	clear(corpus.documentPaths)
	for rows.Next() {
		var document documentMeta
		if err := rows.Scan(
			&document.ID, &document.Path, &document.Name, &document.Extension,
			&document.Title, &document.Course, &document.Kind, &document.Status,
			&document.Visibility, &document.Sequence, &document.Size,
			&document.ModTimeNS, &document.SHA256,
		); err != nil {
			return err
		}
		document.Path = cleanCorpusPath(document.Path)
		if document.Name == "" {
			document.Name = path.Base(document.Path)
		}
		if document.Title == "" {
			document.Title = strings.TrimSuffix(document.Name, document.Extension)
		}
		corpus.documents = append(corpus.documents, document)
		corpus.documentPaths[document.Path] = document
	}
	if err := rows.Err(); err != nil {
		return err
	}
	values := []string{corpus.label, corpus.description, corpus.generated}
	for _, document := range corpus.documents {
		values = append(values,
			fmt.Sprintf("%d", document.ID), document.Path, document.Title,
			fmt.Sprintf("%d", document.ModTimeNS), document.SHA256,
		)
	}
	corpus.revision = previewDigest(values...)
	return nil
}

func (corpus *corpus) close() error {
	if corpus == nil || corpus.db == nil {
		return nil
	}
	return corpus.db.Close()
}

func cleanCorpusPath(value string) string {
	value = strings.TrimPrefix(filepath.ToSlash(value), "/")
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

func (corpus *corpus) courses() []courseSummary {
	counts := make(map[string]int)
	for _, document := range corpus.documents {
		name := strings.TrimSpace(document.Course)
		if name == "" {
			name = "Unclassified"
		}
		counts[name]++
	}
	result := make([]courseSummary, 0, len(counts))
	for name, count := range counts {
		result = append(result, courseSummary{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func (corpus *corpus) documentsForCourse(course string) []documentMeta {
	result := make([]documentMeta, 0)
	for _, document := range corpus.documents {
		name := strings.TrimSpace(document.Course)
		if name == "" {
			name = "Unclassified"
		}
		if name == course {
			result = append(result, document)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence < result[j].Sequence
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
	})
	return result
}

func (corpus *corpus) document(ctx context.Context, id int64) (documentRecord, error) {
	visibility := `'user'`
	if corpus.hasVisibility {
		visibility = "visibility"
	}
	var document documentRecord
	err := corpus.db.QueryRowContext(ctx, `
		SELECT id, path, name, extension, title, course, kind, status, `+visibility+`,
		       sequence, size, mod_time_ns, sha256, content
		FROM documents WHERE id=?`, id).Scan(
		&document.ID, &document.Path, &document.Name, &document.Extension,
		&document.Title, &document.Course, &document.Kind, &document.Status,
		&document.Visibility, &document.Sequence, &document.Size,
		&document.ModTimeNS, &document.SHA256, &document.Content,
	)
	document.Path = cleanCorpusPath(document.Path)
	return document, err
}

func (corpus *corpus) search(ctx context.Context, query string, limit int) ([]documentMeta, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 250
	}
	visibility := `'user'`
	if corpus.hasVisibility {
		visibility = "visibility"
	}
	rows, err := corpus.db.QueryContext(ctx, `
		SELECT id, path, name, extension, title, course, kind, status, `+visibility+`,
		       sequence, size, mod_time_ns, sha256
		FROM documents
		WHERE instr(lower(path || char(10) || title || char(10) || search_text || char(10) || content), lower(?)) > 0
		ORDER BY course, kind, sequence, title LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []documentMeta
	for rows.Next() {
		var document documentMeta
		if err := rows.Scan(
			&document.ID, &document.Path, &document.Name, &document.Extension,
			&document.Title, &document.Course, &document.Kind, &document.Status,
			&document.Visibility, &document.Sequence, &document.Size,
			&document.ModTimeNS, &document.SHA256,
		); err != nil {
			return nil, err
		}
		document.Path = cleanCorpusPath(document.Path)
		result = append(result, document)
	}
	return result, rows.Err()
}

func (corpus *corpus) expandedSource(ctx context.Context, document documentRecord) string {
	source := corpus.expandInputs(ctx, document.Path, document.Content, nil, 0)
	extension := path.Ext(document.Path)
	stem := strings.TrimSuffix(document.Path, extension)
	if !strings.EqualFold(extension, ".tex") || strings.HasSuffix(stem, "-tables") {
		return source
	}
	companionPath := stem + "-tables" + extension
	metadata, exists := corpus.documentPaths[companionPath]
	if !exists {
		return source
	}
	companion, err := corpus.document(ctx, metadata.ID)
	if err != nil {
		return source
	}
	definitions := corpus.expandInputs(ctx, companion.Path, companion.Content, nil, 0)
	return latexpdf.SupplementTableMacros(source, definitions)
}

func (corpus *corpus) expandInputs(ctx context.Context, documentPath, source string, seen map[string]bool, depth int) string {
	if depth >= 8 {
		return source
	}
	if seen == nil {
		seen = make(map[string]bool)
	}
	seen[documentPath] = true
	var output strings.Builder
	for position := 0; position < len(source); {
		next, command := nextSourceInclude(source, position)
		if next < 0 {
			output.WriteString(source[position:])
			break
		}
		afterCommand := next + len(command)
		if !latexpdf.CommandBoundary(source, afterCommand) {
			output.WriteString(source[position:afterCommand])
			position = afterCommand
			continue
		}
		output.WriteString(source[position:next])
		cursor := latexpdf.SkipSpace(source, afterCommand)
		inputName := ""
		inputEnd := cursor
		if cursor < len(source) && source[cursor] == '{' {
			if value, end, ok := latexpdf.ParseBraced(source, cursor); ok {
				inputName, inputEnd = strings.TrimSpace(value), end
			}
		} else {
			for inputEnd < len(source) && !unicode.IsSpace(rune(source[inputEnd])) {
				inputEnd++
			}
			inputName = source[cursor:inputEnd]
		}
		if inputName == "" {
			output.WriteString(source[next:afterCommand])
			position = afterCommand
			continue
		}
		candidate := cleanCorpusPath(path.Join(path.Dir(documentPath), inputName))
		candidates := []string{candidate}
		if path.Ext(candidate) == "" {
			candidates = append(candidates, candidate+".tex")
		}
		var included documentRecord
		for _, candidatePath := range candidates {
			metadata, exists := corpus.documentPaths[candidatePath]
			if !exists || seen[candidatePath] {
				continue
			}
			loaded, err := corpus.document(ctx, metadata.ID)
			if err == nil {
				included = loaded
				break
			}
		}
		if included.ID == 0 {
			output.WriteString(source[next:inputEnd])
		} else {
			nextSeen := make(map[string]bool, len(seen)+1)
			for key, value := range seen {
				nextSeen[key] = value
			}
			output.WriteString(corpus.expandInputs(ctx, included.Path, included.Content, nextSeen, depth+1))
		}
		position = inputEnd
	}
	return output.String()
}

func nextSourceInclude(source string, start int) (int, string) {
	best := -1
	command := ""
	for _, candidate := range []string{`\input`, `\include`} {
		position := strings.Index(source[start:], candidate)
		if position < 0 {
			continue
		}
		position += start
		if best < 0 || position < best {
			best = position
			command = candidate
		}
	}
	return best, command
}
