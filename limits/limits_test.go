package limits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductRejectsOversizeAndOverflow(t *testing.T) {
	for _, dims := range [][]int{{0, 1, 4}, {-1, 1, 4}, {1 << 30, 1 << 30, 4}, {4097, 4096, 4}} {
		if _, ok := Product("test bitmap", PixelBytes, dims...); ok {
			t.Fatalf("accepted %v", dims)
		}
	}
	if n, ok := RGBA("test bitmap", 4096, 4096); !ok || n != PixelBytes {
		t.Fatalf("boundary = %d, %v", n, ok)
	}
}

func TestReadFileLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview")
	if err := os.WriteFile(path, []byte("small preview"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(path); err != nil || string(data) != "small preview" {
		t.Fatalf("small preview: %q, %v", data, err)
	}
	if err := os.Truncate(path, FileBytes+1); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(path); err == nil || data != nil {
		t.Fatal("oversized sparse file was read")
	}
	if _, err := ReadFile(filepath.Dir(path)); err == nil {
		t.Fatal("accepted a directory")
	}
}

func TestDiagnosticLogBounded(t *testing.T) {
	for i := 0; i < 1200; i++ {
		Report("test", "%d %s", i, strings.Repeat("x", 2000))
	}
	Report("test", "latest rejection")
	diagnostics.Lock()
	defer diagnostics.Unlock()
	if diagnostics.file == nil {
		t.Fatal("no diagnostic log")
	}
	path := diagnostics.file.Name()
	if filepath.Dir(path) != "/tmp" {
		t.Fatalf("log outside /tmp: %s", path)
	}
	info, err := diagnostics.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > LogBytes || info.Mode().Perm() != 0600 {
		t.Fatalf("log size/mode = %d/%o", info.Size(), info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "latest rejection") {
		t.Fatalf("missing latest diagnostic: %v", err)
	}
}
