package terminfo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateMatchesReferenceEntry(t *testing.T) {
	data := Generate(80, 24, 8)
	got := sha256.Sum256(data)
	const want = "0d0618247c9667e44ac9e41c0964886d745822ae4976ca9403bce23e334e939c"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("generated entry: size=%d sha256=%x, want size=2759 sha256=%s", len(data), got, want)
	}
}

func TestInstallWritesNameAndAlias(t *testing.T) {
	root := t.TempDir()
	path, err := Install(root, 80, 24, 8)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "s", Name)
	if path != wantPath {
		t.Fatalf("path=%q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := os.ReadFile(filepath.Join(root, "s", Alias))
	if err != nil {
		t.Fatal(err)
	}
	if string(alias) != string(data) {
		t.Fatal("alias differs from primary entry")
	}
}
