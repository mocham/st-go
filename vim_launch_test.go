package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseVimInvocation(t *testing.T) {
	active, opts, file, err := parseVimInvocation([]string{"vim", "-c", "set number", "--", "notes.md"})
	if err != nil || !active || file != "notes.md" || !reflect.DeepEqual(opts, []string{"-c", "set number"}) {
		t.Fatalf("active=%v opts=%v file=%q err=%v", active, opts, file, err)
	}
	active, _, _, err = parseVimInvocation([]string{"-e", "vim", "notes.md"})
	if err != nil || active {
		t.Fatalf("ordinary st arguments recognized as vim mode")
	}
	if active, _, _, err = parseVimInvocation([]string{"vim"}); !active || err == nil {
		t.Fatalf("missing file: active=%v err=%v", active, err)
	}
}

func TestBuildVimLaunch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new file.md")
	spec, err := buildVimLaunchWithLookPath([]string{"-c", "set number"}, file,
		func(name string) (string, error) { return "/usr/bin/vim", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/vim", "-c", "set number", "--", "new file.md"}
	if !reflect.DeepEqual(spec.Command, want) || spec.Dir != dir || spec.Title != "emulated-vim" {
		t.Fatalf("spec=%#v want command=%v dir=%q", spec, want, dir)
	}
	_, err = buildVimLaunchWithLookPath(nil, file,
		func(name string) (string, error) { return "", errors.New("missing") })
	if err == nil {
		t.Fatal("missing vim was accepted")
	}
}

func TestEmulatedVimRect(t *testing.T) {
	if got := emulatedVimRect(1920, 1080, 18, 30, 64); got != (windowRect{0, 0, 1920, 1016}) {
		t.Fatalf("geometry=%#v", got)
	}
	if got := emulatedVimRect(40, 50, 18, 30, 64); got != (windowRect{0, 0, 40, 30}) {
		t.Fatalf("small geometry=%#v", got)
	}
}

func TestEmulatedVimTitleIsLocked(t *testing.T) {
	trm := &Terminal{title: "emulated-vim", iconTitle: "emulated-vim", lockTitle: true}
	trm.setTitle("changed-by-vim")
	trm.SetIconTitle("changed-by-vim")
	if trm.title != "emulated-vim" || trm.iconTitle != "emulated-vim" {
		t.Fatalf("titles changed to %q/%q", trm.title, trm.iconTitle)
	}
}
