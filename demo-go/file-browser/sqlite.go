package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const sqliteRowsPerPage = 100

type sqliteEntryKind uint8

const (
	sqliteEntryParent sqliteEntryKind = iota
	sqliteEntryTable
	sqliteEntryRecord
	sqliteEntryPrevious
	sqliteEntryNext
)

type sqliteTable struct {
	name       string
	columns    []string
	rowCount   int64
	countKnown bool
}

type sqliteRecord struct {
	number int64
	values []string
}

type sqliteEntry struct {
	kind        sqliteEntryKind
	label       string
	tableIndex  int
	recordIndex int
	offset      int64
}

// sqliteCtx is a read-only virtual directory rooted at one SQLite database.
// Tables are directory-like entries; records are loaded one bounded page at a
// time and shown as ordinary selectable entries.
type sqliteCtx struct {
	file          string
	label         string
	db            *sql.DB
	tables        []sqliteTable
	tableIndex    int
	offset        int64
	records       []sqliteRecord
	entries       map[string]sqliteEntry
	returnDir     string
	returnEntry   string
	returnArchive bool
}

func isSQLitePath(path string) bool {
	if hasSQLiteExtension(path) {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	var header [16]byte
	n, _ := file.Read(header[:])
	return n == len(header) && string(header[:]) == "SQLite format 3\x00"
}

func hasSQLiteExtension(path string) bool {
	return hasSuffixAny(strings.ToLower(path), ".sqlite3", ".sqlite", ".db", ".db3", ".s3db")
}

func openSQLiteCtx(file, label, returnDir, returnEntry string, returnArchive bool) (*sqliteCtx, error) {
	absolute, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	dsn := url.URL{Scheme: "file", Path: absolute}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Set("_query_only", "1")
	query.Set("_busy_timeout", "3000")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := &sqliteCtx{
		file: absolute, label: label, db: db, tableIndex: -1,
		returnDir: returnDir, returnEntry: returnEntry, returnArchive: returnArchive,
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(queryCtx); err != nil {
		db.Close()
		return nil, err
	}
	if err := ctx.loadTables(queryCtx); err != nil {
		db.Close()
		return nil, err
	}
	return ctx, nil
}

func (ctx *sqliteCtx) close() {
	if ctx != nil && ctx.db != nil {
		ctx.db.Close()
		ctx.db = nil
	}
}

func (ctx *sqliteCtx) loadTables(queryCtx context.Context) error {
	rows, err := ctx.db.QueryContext(queryCtx, `
		SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name COLLATE NOCASE, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	ctx.tables = nil
	for rows.Next() {
		var table sqliteTable
		if err := rows.Scan(&table.name); err != nil {
			return err
		}
		ctx.tables = append(ctx.tables, table)
	}
	return rows.Err()
}

func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (ctx *sqliteCtx) loadTablePage(tableIndex int, offset int64) error {
	if tableIndex < 0 || tableIndex >= len(ctx.tables) {
		return fmt.Errorf("table is no longer available")
	}
	if offset < 0 {
		offset = 0
	}
	table := &ctx.tables[tableIndex]
	queryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !table.countKnown {
		query := `SELECT COUNT(*) FROM ` + quoteSQLiteIdentifier(table.name)
		if err := ctx.db.QueryRowContext(queryCtx, query).Scan(&table.rowCount); err != nil {
			return err
		}
		table.countKnown = true
	}
	if offset >= table.rowCount && table.rowCount > 0 {
		offset = ((table.rowCount - 1) / sqliteRowsPerPage) * sqliteRowsPerPage
	}
	query := `SELECT * FROM ` + quoteSQLiteIdentifier(table.name) + ` LIMIT ? OFFSET ?`
	rows, err := ctx.db.QueryContext(queryCtx, query, sqliteRowsPerPage, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	table.columns = append(table.columns[:0], columns...)
	ctx.records = nil
	for rows.Next() {
		raw := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range raw {
			destinations[index] = &raw[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		record := sqliteRecord{number: offset + int64(len(ctx.records)) + 1, values: make([]string, len(raw))}
		for index, value := range raw {
			record.values[index] = formatSQLiteValue(value)
		}
		ctx.records = append(ctx.records, record)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ctx.tableIndex = tableIndex
	ctx.offset = offset
	return nil
}

func formatSQLiteValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	case string:
		return safeSQLiteText(typed)
	case []byte:
		preview := typed
		if len(preview) > 16 {
			preview = preview[:16]
		}
		suffix := ""
		if len(preview) < len(typed) {
			suffix = "…"
		}
		return fmt.Sprintf("<blob %d bytes: %s%s>", len(typed), hex.EncodeToString(preview), suffix)
	default:
		return safeSQLiteText(fmt.Sprint(typed))
	}
}

func safeSQLiteText(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch character {
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		case '\x1b':
			output.WriteString(`\E`)
		default:
			if character < 0x20 || character == 0x7f {
				fmt.Fprintf(&output, `\x%02x`, character)
			} else {
				output.WriteRune(character)
			}
		}
	}
	return output.String()
}

func (ctx *sqliteCtx) atTableRoot() bool { return ctx.tableIndex < 0 }

func (ctx *sqliteCtx) table() *sqliteTable {
	if ctx == nil || ctx.tableIndex < 0 || ctx.tableIndex >= len(ctx.tables) {
		return nil
	}
	return &ctx.tables[ctx.tableIndex]
}

func (ctx *sqliteCtx) rebuildEntries() []string {
	ctx.entries = make(map[string]sqliteEntry)
	keys := []string{"sqlite:parent"}
	ctx.entries[keys[0]] = sqliteEntry{kind: sqliteEntryParent, label: ".."}
	if ctx.atTableRoot() {
		indices := make([]int, len(ctx.tables))
		for index := range indices {
			indices[index] = index
		}
		sort.SliceStable(indices, func(left, right int) bool {
			return strings.ToLower(ctx.tables[indices[left]].name) < strings.ToLower(ctx.tables[indices[right]].name)
		})
		for _, tableIndex := range indices {
			key := fmt.Sprintf("sqlite:table:%d", tableIndex)
			ctx.entries[key] = sqliteEntry{kind: sqliteEntryTable, label: safeSQLiteText(ctx.tables[tableIndex].name), tableIndex: tableIndex}
			keys = append(keys, key)
		}
		return keys
	}
	if ctx.offset > 0 {
		previous := ctx.offset - sqliteRowsPerPage
		if previous < 0 {
			previous = 0
		}
		key := "sqlite:previous"
		ctx.entries[key] = sqliteEntry{kind: sqliteEntryPrevious, label: fmt.Sprintf("previous rows %d–%d", previous+1, ctx.offset), offset: previous}
		keys = append(keys, key)
	}
	for recordIndex := range ctx.records {
		key := fmt.Sprintf("sqlite:record:%d", recordIndex)
		ctx.entries[key] = sqliteEntry{kind: sqliteEntryRecord, label: ctx.recordLabel(recordIndex), recordIndex: recordIndex}
		keys = append(keys, key)
	}
	if table := ctx.table(); table != nil && ctx.offset+int64(len(ctx.records)) < table.rowCount {
		key := "sqlite:next"
		last := ctx.offset + 2*sqliteRowsPerPage
		if last > table.rowCount {
			last = table.rowCount
		}
		ctx.entries[key] = sqliteEntry{kind: sqliteEntryNext, label: fmt.Sprintf("next rows %d–%d", ctx.offset+sqliteRowsPerPage+1, last), offset: ctx.offset + sqliteRowsPerPage}
		keys = append(keys, key)
	}
	return keys
}

func (ctx *sqliteCtx) recordLabel(recordIndex int) string {
	record := ctx.records[recordIndex]
	parts := []string{fmt.Sprintf("#%d", record.number)}
	table := ctx.table()
	for index, value := range record.values {
		if index >= 3 {
			break
		}
		name := fmt.Sprintf("c%d", index+1)
		if table != nil && index < len(table.columns) {
			name = safeSQLiteText(table.columns[index])
		}
		parts = append(parts, name+"="+shorten(value, 32))
	}
	return strings.Join(parts, "  ")
}

func (b *Browser) isSQLiteMode() bool { return b.sqlite != nil }

func (b *Browser) sqliteEntry(key string) (sqliteEntry, bool) {
	if b.sqlite == nil {
		return sqliteEntry{}, false
	}
	entry, ok := b.sqlite.entries[key]
	return entry, ok
}

func (b *Browser) buildSQLiteList() { b.files = b.sqlite.rebuildEntries() }

func (b *Browser) refreshSQLite() error {
	ctx := b.sqlite
	tableName := ""
	if table := ctx.table(); table != nil {
		tableName = table.name
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ctx.loadTables(queryCtx); err != nil {
		return err
	}
	if tableName == "" {
		ctx.tableIndex = -1
		ctx.records = nil
		return nil
	}
	for index := range ctx.tables {
		if ctx.tables[index].name == tableName {
			return ctx.loadTablePage(index, ctx.offset)
		}
	}
	ctx.tableIndex = -1
	ctx.records = nil
	b.dirLabel = ctx.label
	return nil
}

func (b *Browser) enterSQLite(file, label, returnEntry string) {
	ctx, err := openSQLiteCtx(file, label, b.dir, returnEntry, b.isArchiveMode())
	if err != nil {
		b.status = "Cannot open SQLite database " + displayText(baseName(label))
		b.drawStatus()
		b.flush()
		return
	}
	b.sqlite = ctx
	b.dirLabel = ctx.label
	b.resetSQLiteView()
	b.status = "Browsing SQLite database " + displayText(baseName(label))
	b.renderAll()
}

func (b *Browser) resetSQLiteView() {
	b.resetClick()
	b.idx = 0
	b.viewTop = 0
	b.page = 1
	b.previewPageTotal = 0
	b.buildSQLiteList()
}

func (b *Browser) enterSQLiteEntry(key string) {
	entry, ok := b.sqliteEntry(key)
	if !ok {
		return
	}
	switch entry.kind {
	case sqliteEntryParent:
		b.goSQLiteParent()
	case sqliteEntryTable:
		if err := b.sqlite.loadTablePage(entry.tableIndex, 0); err != nil {
			b.status = "Cannot read SQLite table: " + safeSQLiteText(err.Error())
			b.drawStatus()
			b.flush()
			return
		}
		b.dirLabel = b.sqlite.label + "::" + b.sqlite.tables[entry.tableIndex].name
		b.resetSQLiteView()
		b.status = "Browsing table " + safeSQLiteText(b.sqlite.tables[entry.tableIndex].name)
		b.renderAll()
	case sqliteEntryPrevious, sqliteEntryNext:
		if err := b.sqlite.loadTablePage(b.sqlite.tableIndex, entry.offset); err != nil {
			b.status = "Cannot read SQLite records: " + safeSQLiteText(err.Error())
			b.drawStatus()
			b.flush()
			return
		}
		b.resetSQLiteView()
		b.status = fmt.Sprintf("Rows %d–%d", b.sqlite.offset+1, b.sqlite.offset+int64(len(b.sqlite.records)))
		b.renderAll()
	}
}

func (b *Browser) goSQLiteParent() {
	if b.sqlite == nil {
		return
	}
	if !b.sqlite.atTableRoot() {
		b.sqlite.tableIndex = -1
		b.sqlite.offset = 0
		b.sqlite.records = nil
		b.dirLabel = b.sqlite.label
		b.resetSQLiteView()
		b.status = "Browsing SQLite tables"
		b.renderAll()
		return
	}
	ctx := b.sqlite
	b.sqlite = nil
	ctx.close()
	if ctx.returnArchive && b.isArchiveMode() {
		b.dirLabel = b.archiveLabel()
		b.resetArchiveView()
		for index, entry := range b.files {
			if entry == ctx.returnEntry {
				b.idx = index
				break
			}
		}
		b.status = "Browsing " + displayText(b.archiveLabel())
		b.renderAll()
		return
	}
	b.changeDirectory(ctx.returnDir, ctx.returnEntry)
}

func (b *Browser) updateSQLiteInfo(key string) {
	entry, ok := b.sqliteEntry(key)
	if !ok {
		b.infoName, b.infoKind, b.infoSize, b.infoMode, b.infoTime = "-", "SQLite entry", "-", "-", "-"
		return
	}
	b.infoName = entry.label
	b.infoTime = "-"
	switch entry.kind {
	case sqliteEntryParent:
		b.infoKind, b.infoSize, b.infoMode = "parent directory", "-", "-"
	case sqliteEntryTable:
		table := b.sqlite.tables[entry.tableIndex]
		b.infoKind, b.infoMode = "SQLite table", "read-only"
		if table.countKnown {
			b.infoSize = fmt.Sprintf("%d rows", table.rowCount)
		} else {
			b.infoSize = "Enter to list records"
		}
	case sqliteEntryRecord:
		record := b.sqlite.records[entry.recordIndex]
		b.infoKind = "SQLite record"
		b.infoSize = fmt.Sprintf("row %d", record.number)
		b.infoMode = fmt.Sprintf("%d columns", len(record.values))
	case sqliteEntryPrevious, sqliteEntryNext:
		b.infoKind, b.infoSize, b.infoMode = "record page", entry.label, "read-only"
	}
}

func (b *Browser) drawSQLitePreview(key string) {
	b.previewWasGraphic = false
	b.clearPreview()
	entry, ok := b.sqliteEntry(key)
	if !ok {
		b.drawPreviewMessage("SQLITE", "Unknown entry", "Refresh the database view.")
		return
	}
	switch entry.kind {
	case sqliteEntryParent:
		b.drawPreviewMessage("SQLITE DATABASE", displayText(baseName(b.sqlite.label)), "Backspace or Enter returns to the containing view.")
	case sqliteEntryTable:
		table := b.sqlite.tables[entry.tableIndex]
		b.drawPreviewTitle("SQLITE TABLE  " + safeSQLiteText(table.name))
		b.drawText(4, 3, b.previewW-4, resetStyle, "Enter to list records")
		if table.countKnown {
			b.drawText(5, 3, b.previewW-4, resetStyle, fmt.Sprintf("rows     %d", table.rowCount))
		}
	case sqliteEntryRecord:
		table := b.sqlite.table()
		record := b.sqlite.records[entry.recordIndex]
		title := fmt.Sprintf("SQLITE RECORD  %d", record.number)
		if table != nil {
			title += "  " + safeSQLiteText(table.name)
		}
		b.drawPreviewTitle(title)
		row := 4
		for index, value := range record.values {
			if row >= b.rows {
				remaining := len(record.values) - index
				b.drawText(b.rows-1, 3, b.previewW-4, dimStyle, fmt.Sprintf("… %d more columns", remaining))
				break
			}
			name := fmt.Sprintf("column %d", index+1)
			if table != nil && index < len(table.columns) {
				name = safeSQLiteText(table.columns[index])
			}
			b.drawText(row, 3, b.previewW-4, resetStyle, fmt.Sprintf("%-20s  %s", shorten(name, 20), value))
			row++
		}
	case sqliteEntryPrevious, sqliteEntryNext:
		b.drawPreviewMessage("SQLITE RECORDS", entry.label, "Enter to load this page.")
	}
}
