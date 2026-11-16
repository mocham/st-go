//go:build linux && cgo && ffmpeg_static && !purego

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeDRMBufferRetirementAndVTTransitions(t *testing.T) {
	metadata, err := filepath.Abs("../../third_party/libdrm/lib/pkgconfig/libdrm.pc")
	if err != nil {
		t.Fatal(err)
	}
	flags, err := exec.Command("pkg-config", "--static", "--cflags", "--libs", metadata).Output()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "drm-lifecycle")
	args := []string{"-static", "-std=c11", "-D_GNU_SOURCE", "-Wall", "-Wextra", "-Werror", "testdata/drm-lifecycle.c", "-o", program}
	args = append(args, strings.Fields(string(flags))...)
	if output, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
		t.Fatalf("compile native lifecycle test: %v\n%s", err, output)
	}
	if output, err := exec.Command(program).CombinedOutput(); err != nil {
		t.Fatalf("native lifecycle: %v\n%s", err, output)
	}
}
