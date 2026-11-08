package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecentDatabasesPersistInMRUOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first := filepath.Join(home, "first.sqlite3")
	second := filepath.Join(home, "second.sqlite3")
	for _, filePath := range []string{first, second} {
		if err := os.WriteFile(filePath, []byte("db"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordRecentDatabases([]string{first, second, first}); err != nil {
		t.Fatal(err)
	}
	if err := recordRecentDatabases([]string{second}); err != nil {
		t.Fatal(err)
	}
	got, err := loadRecentDatabases()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{second, first}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent databases = %#v, want %#v", got, want)
	}
	filePath, err := recentDatabasesPath()
	if err != nil {
		t.Fatal(err)
	}
	if filePath != filepath.Join(home, ".cache", "gols.json") {
		t.Fatalf("recent file = %q", filePath)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recent file mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	var state recentDatabaseFile
	if err := json.Unmarshal(data, &state); err != nil || !reflect.DeepEqual(state.Databases, want) {
		t.Fatalf("recent JSON = %s, error = %v", data, err)
	}
}

func TestRecentDatabasesAreBoundedAndRecoverMalformedCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var paths []string
	for index := 0; index < maxRecentDatabases+5; index++ {
		filePath := filepath.Join(home, "db", string(rune('a'+index))+".sqlite3")
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filePath)
	}
	if err := recordRecentDatabases(paths); err != nil {
		t.Fatal(err)
	}
	got, err := loadRecentDatabases()
	if err != nil || len(got) != maxRecentDatabases {
		t.Fatalf("bounded recent list has %d entries, error = %v", len(got), err)
	}
	cachePath, err := recentDatabasesPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordRecentDatabases([]string{paths[1]}); err != nil {
		t.Fatal(err)
	}
	got, err = loadRecentDatabases()
	if err != nil || !reflect.DeepEqual(got, []string{paths[1]}) {
		t.Fatalf("recovered recent list = %#v, error = %v", got, err)
	}
}

func TestRecentDatabaseSelectionUpdatesRecorder(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	var recorded []string
	output := &testOutput{}
	browser := &Browser{
		fd: -1, out: output, corpora: []*corpus{opened}, visible: 20,
		view:       viewState{kind: viewDatabases, corpusIndex: -1},
		recentMode: true,
		recentRecorder: func(paths []string) error {
			recorded = append([]string(nil), paths...)
			return nil
		},
	}
	browser.buildEntries()
	browser.enterSelected()
	if browser.view.kind != viewCourses || !reflect.DeepEqual(recorded, []string{opened.file}) {
		t.Fatalf("database selection: view=%d recorded=%#v", browser.view.kind, recorded)
	}
}

func TestEmptyRecentDatabaseViewRendersChooser(t *testing.T) {
	output := &testOutput{}
	browser := &Browser{fd: -1, out: output, recentMode: true}
	if err := browser.setup(); err != nil {
		t.Fatal(err)
	}
	defer browser.cleanup()
	browser.renderAll()
	if rendered := output.String(); !strings.Contains(rendered, "recent databases") || !strings.Contains(rendered, "RECENT DATABASES") || !strings.Contains(rendered, "No recent databases") {
		t.Fatalf("empty recent chooser was not rendered: %q", rendered)
	}
}

func TestDatabaseCandidatesRequireExplicitInputs(t *testing.T) {
	paths, err := databaseCandidates(nil)
	if err != nil || len(paths) != 0 {
		t.Fatalf("empty database candidates = %#v, %v", paths, err)
	}
}
