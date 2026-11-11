package main

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"st-go/pdfmeta"
)

func generatedDjVu(t *testing.T) string {
	t.Helper()
	c44, err := exec.LookPath("c44")
	if err != nil {
		t.Skip("c44 is not installed")
	}
	directory := t.TempDir()
	ppm := filepath.Join(directory, "source.ppm")
	var source bytes.Buffer
	source.WriteString("P6\n64 48\n255\n")
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			source.Write([]byte{byte(x * 4), byte(y * 5), 96})
		}
	}
	if err := os.WriteFile(ppm, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "generated.djvu")
	if output, err := exec.Command(c44, ppm, path).CombinedOutput(); err != nil {
		t.Fatalf("c44: %v\n%s", err, output)
	}
	return path
}

func addDjVuMetadata(t *testing.T, path string) {
	t.Helper()
	djvused, err := exec.LookPath("djvused")
	if err != nil {
		t.Skip("djvused is not installed")
	}
	directory := filepath.Dir(path)
	text := filepath.Join(directory, "text.sexpr")
	outline := filepath.Join(directory, "outline.sexpr")
	if err := os.WriteFile(text, []byte(`(page 0 0 64 48
 (column 1 1 63 47
  (region 1 1 63 47
   (para 1 1 63 47
    (line 1 25 63 45
     (word 1 25 30 45 "Hello")
     (word 32 25 63 45 "world"))))))
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outline, []byte(`(bookmarks
 ("First" "#1")
 ("Nested" "#1" ("Child" "#1")))
`), 0600); err != nil {
		t.Fatal(err)
	}
	script := "select 1; set-txt " + text + "; set-outline " + outline
	if output, err := exec.Command(djvused, "-s", "-e", script, path).CombinedOutput(); err != nil {
		t.Fatalf("djvused: %v\n%s", err, output)
	}
}

func TestDjVuLibreDocumentAndRaster(t *testing.T) {
	path := generatedDjVu(t)
	validated, err := validateDjVu(path)
	if err != nil || validated != path {
		t.Fatalf("validateDjVu = %q, %v", validated, err)
	}
	document, err := openDjVu(path, 0xf7f5ef)
	if err != nil {
		t.Fatal(err)
	}
	defer document.close()
	if document.pages != 1 {
		t.Fatalf("pages = %d", document.pages)
	}
	output := filepath.Join(t.TempDir(), "page.png")
	width, height, err := document.renderPNG(1, output)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if width != 64 || height != 48 || image.Bounds().Dx() != width || image.Bounds().Dy() != height {
		t.Fatalf("rendered dimensions = %dx%d, image %v", width, height, image.Bounds())
	}
	red, green, blue, alpha := image.At(width/2, height/2).RGBA()
	if alpha != 0xffff {
		t.Fatalf("rendered alpha = %#x", alpha)
	}
	if red <= blue || green <= blue {
		t.Fatalf("rendered channels appear swapped: r=%#x g=%#x b=%#x", red, green, blue)
	}
	text, truncated, err := document.pageText(1)
	if err != nil || text != "" || truncated {
		t.Fatalf("blank hidden text = %q, %t, %v", text, truncated, err)
	}
	outline, truncated, err := document.outline()
	if err != nil || len(outline) != 0 || truncated {
		t.Fatalf("blank outline = %#v, %t, %v", outline, truncated, err)
	}
}

func TestDjVuValidationRejectsOtherFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.djvu")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDjVu(path); err == nil {
		t.Fatal("PDF was accepted as DjVu")
	}
}

func TestDjVuLibreHiddenTextAndOutline(t *testing.T) {
	path := generatedDjVu(t)
	addDjVuMetadata(t, path)
	document, err := openDjVu(path, 0xffffff)
	if err != nil {
		t.Fatal(err)
	}
	defer document.close()
	text, truncated, err := document.pageText(1)
	if err != nil || truncated || !strings.Contains(cleanPanelText(text), "Hello world") {
		t.Fatalf("hidden text = %q, %t, %v", text, truncated, err)
	}
	outline, truncated, err := document.outline()
	want := []pdfmeta.Entry{{Title: "First", Page: 1}, {Title: "Nested", Page: 1}, {Title: "Child", Page: 1, Level: 1}}
	if err != nil || truncated || !reflect.DeepEqual(outline, want) {
		t.Fatalf("outline = %#v, %t, %v", outline, truncated, err)
	}
}

func TestDjVuLibreOutlineComponentID(t *testing.T) {
	djvm, err := exec.LookPath("djvm")
	if err != nil {
		t.Skip("djvm is not installed")
	}
	djvused, err := exec.LookPath("djvused")
	if err != nil {
		t.Skip("djvused is not installed")
	}
	directory := t.TempDir()
	first := filepath.Join(directory, "first.djvu")
	second := filepath.Join(directory, "second.djvu")
	for source, target := range map[string]string{generatedDjVu(t): first, generatedDjVu(t): second} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(directory, "bundle.djvu")
	if output, err := exec.Command(djvm, "-c", bundle, first, second).CombinedOutput(); err != nil {
		t.Fatalf("djvm: %v\n%s", err, output)
	}
	outlinePath := filepath.Join(directory, "outline.sexpr")
	if err := os.WriteFile(outlinePath, []byte("(bookmarks (\"Second\" \"#second.djvu\"))\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(djvused, "-s", "-e", "set-outline "+outlinePath, bundle).CombinedOutput(); err != nil {
		t.Fatalf("djvused: %v\n%s", err, output)
	}
	document, err := openDjVu(bundle, 0xffffff)
	if err != nil {
		t.Fatal(err)
	}
	defer document.close()
	outline, truncated, err := document.outline()
	want := []pdfmeta.Entry{{Title: "Second", Page: 2}}
	if err != nil || truncated || !reflect.DeepEqual(outline, want) {
		t.Fatalf("component-ID outline = %#v, %t, %v", outline, truncated, err)
	}
}
