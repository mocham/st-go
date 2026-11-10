package frontend

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Use make's question mode in a synthetic build tree: no recipe (including
// recursive make, compilers, or downloads) may run during this regression.
func TestMakefileReusesThirdPartyArchives(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	dir := t.TempDir()
	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	write := func(path string, data []byte, when time.Time) {
		t.Helper()
		path = filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	artifacts := []string{
		"third_party/freetype/libfreetype.a", "third_party/stb/stb_image.o",
		"third_party/poppler/lib/libz.a", "third_party/poppler/lib/libpng16.a",
		"third_party/jpeg/lib/libjpeg.a", "third_party/poppler/lib/libpoppler.a",
		"third_party/webp/lib/libwebp.a", "third_party/webp/lib/libwebpdemux.a",
		"third_party/webp/webp_anim.o", "third_party/pdf_bridge.o",
		"third_party/alsa/lib/libasound.a", "third_party/alsa/snd.o",
	}
	for _, path := range append(append([]string{}, artifacts...),
		"third_party/stb/stb_image.h", "third_party_wrapper/stb_image.c",
		"third_party_wrapper/webp_anim.c", "third_party_wrapper/alsa_snd.c",
		"third_party_wrapper/pdf_bridge.cpp", "pdf_bridge.h", "go.mod", "go.sum", "Monaco_Linux.ttf", "st", "file-browser") {
		write(path, nil, old)
	}
	// Simulate a checkout or an unrelated edit making Makefile newer than all
	// cached artifacts. Only application binaries should become out of date.
	write("Makefile", makefile, old.Add(time.Hour))
	question := func(target string, want int) {
		t.Helper()
		cmd := exec.Command(makePath, "--no-print-directory", "-r", "-q", target)
		cmd.Dir = dir
		cmd.Env = envWithout(envWithout(envWithout(os.Environ(), "MAKEFLAGS"), "MFLAGS"), "MAKEFILES")
		output, err := cmd.CombinedOutput()
		status := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			status = exit.ExitCode()
		}
		if status != want {
			t.Fatalf("make -q %s = %d, want %d (no recipes executed): %s", target, status, want, output)
		}
	}
	for _, artifact := range artifacts {
		question(artifact, 0)
	}
	question("st", 1)
	question("st-web", 1)
	question("st-fb", 1)
	question("file-browser", 1)
	// Real native dependencies still invalidate their consumers.
	write("third_party/jpeg/lib/libjpeg.a", nil, old.Add(30*time.Minute))
	question("third_party/poppler/lib/libpoppler.a", 1)
	write("third_party/jpeg/lib/libjpeg.a", nil, old)
	write("third_party_wrapper/pdf_bridge.cpp", nil, old.Add(30*time.Minute))
	question("third_party/pdf_bridge.o", 1)
	question("third_party/poppler/lib/libpoppler.a", 0)
	// A missing archive must still be scheduled for its first build.
	if err := os.Remove(filepath.Join(dir, "third_party/jpeg/lib/libjpeg.a")); err != nil {
		t.Fatal(err)
	}
	question("third_party/jpeg/lib/libjpeg.a", 1)
}
