package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeSQLitePreviewTestDB(t *testing.T, path string, rows int) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT, note TEXT, payload BLOB);
		CREATE TABLE "odd""table" (value TEXT);
		CREATE TABLE empty_table (id INTEGER);`); err != nil {
		t.Fatal(err)
	}
	transaction, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare(`INSERT INTO authors(id, name, note, payload) VALUES (?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= rows; index++ {
		name := "author " + strings.Repeat("x", index%5)
		note := any(nil)
		if index == 1 {
			name = "Ada Lovelace"
			note = "line one\nline two\x1b[31m"
		}
		if _, err := statement.Exec(index, name, note, []byte{0, 1, byte(index)}); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO "odd""table" VALUES ('quoted identifier')`); err != nil {
		t.Fatal(err)
	}
}

func shortSQLiteTestDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "fbsql-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

func TestSQLiteContextListsTablesRecordsAndPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "general.sqlite3")
	makeSQLitePreviewTestDB(t, path, sqliteRowsPerPage+5)
	ctx, err := openSQLiteCtx(path, path, filepath.Dir(path), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.close()

	if len(ctx.tables) != 3 {
		t.Fatalf("tables = %#v, want three user tables", ctx.tables)
	}
	authors := -1
	quoted := -1
	for index, table := range ctx.tables {
		switch table.name {
		case "authors":
			authors = index
		case `odd"table`:
			quoted = index
		}
	}
	if authors < 0 || quoted < 0 {
		t.Fatalf("table indexes authors=%d quoted=%d", authors, quoted)
	}
	if err := ctx.loadTablePage(authors, 0); err != nil {
		t.Fatal(err)
	}
	if len(ctx.records) != sqliteRowsPerPage || ctx.tables[authors].rowCount != sqliteRowsPerPage+5 {
		t.Fatalf("first page records=%d total=%d", len(ctx.records), ctx.tables[authors].rowCount)
	}
	if got := ctx.records[0].values[1]; got != "Ada Lovelace" {
		t.Fatalf("first name = %q", got)
	}
	if got := ctx.records[0].values[2]; got != `line one\nline two\E[31m` {
		t.Fatalf("escaped note = %q", got)
	}
	if got := ctx.records[0].values[3]; !strings.Contains(got, "<blob 3 bytes:") {
		t.Fatalf("blob = %q", got)
	}
	keys := ctx.rebuildEntries()
	if keys[len(keys)-1] != "sqlite:next" {
		t.Fatalf("last first-page entry = %q, want next page", keys[len(keys)-1])
	}
	if err := ctx.loadTablePage(authors, sqliteRowsPerPage); err != nil {
		t.Fatal(err)
	}
	if len(ctx.records) != 5 {
		t.Fatalf("second page records = %d, want 5", len(ctx.records))
	}
	keys = ctx.rebuildEntries()
	if len(keys) < 2 || keys[1] != "sqlite:previous" {
		t.Fatalf("second-page entries = %#v", keys)
	}
	if err := ctx.loadTablePage(quoted, 0); err != nil {
		t.Fatalf("quoted table name: %v", err)
	}
	if got := ctx.records[0].values[0]; got != "quoted identifier" {
		t.Fatalf("quoted table value = %q", got)
	}
	if _, err := ctx.db.Exec(`CREATE TABLE forbidden (id INTEGER)`); err == nil {
		t.Fatal("read-only preview database accepted CREATE TABLE")
	}
}

func TestSQLiteSignatureDetectionWithoutExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-data")
	makeSQLitePreviewTestDB(t, path, 1)
	if !isSQLitePath(path) {
		t.Fatal("SQLite header was not detected without a filename extension")
	}
	plain := filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(plain, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if !isSQLitePath(plain) {
		t.Fatal("recognized SQLite extension should be offered as a database")
	}
}

func TestSQLiteBrowseTablesRecordsAndBack(t *testing.T) {
	directory := shortSQLiteTestDir(t)
	database := filepath.Join(directory, "library.sqlite3")
	makeSQLitePreviewTestDB(t, database, 2)
	f := startBrowser(t, testEnv(), directory)
	waitContains(t, f, "library.sqlite3", "SQLite database listed")
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "authors", "SQLite tables listed")

	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "name=Ada", "SQLite records listed")
	f.send("\x1b[B")
	waitContains(t, f, "SQLITE RECORD", "SQLite record preview")
	waitContains(t, f, `line one\nline two\E[31m`, "record controls escaped")

	f.send("\x7f")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == database }, "back to SQLite tables")
	f.send("\x7f")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == directory }, "back to containing directory")
}

func TestInvalidSQLiteFailsWithoutLeavingDirectory(t *testing.T) {
	directory := shortSQLiteTestDir(t)
	path := filepath.Join(directory, "broken.db")
	if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), directory)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "Cannot open SQLi", "invalid SQLite status")
	if got := lastHeaderPath(f.output()); got != directory {
		t.Fatalf("header after invalid database = %q, want %q", got, directory)
	}
}
