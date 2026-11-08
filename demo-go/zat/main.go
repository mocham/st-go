// zat is a keyboard-driven PDF viewer using st-go's retained native renderer.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"st-go/limits"
)

type options struct {
	file, background string
	page, zoom       int
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("zat", flag.ContinueOnError)
	f.SetOutput(out)
	f.IntVar(&o.page, "page", 1, "initial page (one-based)")
	f.IntVar(&o.zoom, "zoom", 100, "zoom relative to fit-width (10..500 percent)")
	f.StringVar(&o.background, "bg", "#f7f5ef", "paper/canvas color (#RRGGBB)")
	f.Usage = func() {
		fmt.Fprintln(out, "Usage: zat [options] document.pdf\n\nRun inside st-go. Tab: TOC; Shift+Tab: selectable page text (Ctrl+Shift+C copies).\nj/k scroll, J/K change page, gg/G jump, +/- zoom, :help, q quit.")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 1 {
		return o, fmt.Errorf("one PDF filename is required")
	}
	if o.page < 1 || o.page > limits.PreviewPages {
		return o, fmt.Errorf("-page must be 1..%d", limits.PreviewPages)
	}
	if o.zoom < 10 || o.zoom > 500 {
		return o, fmt.Errorf("-zoom must be 10..500")
	}
	if len(o.background) != 7 || o.background[0] != '#' {
		return o, fmt.Errorf("-bg must be #RRGGBB")
	}
	if _, err := strconv.ParseUint(o.background[1:], 16, 24); err != nil {
		return o, fmt.Errorf("-bg must be #RRGGBB")
	}
	var err error
	o.file, err = validatePDF(f.Arg(0))
	return o, err
}

func validatePDF(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("PDF filename contains a control character")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > limits.FileBytes {
		return "", fmt.Errorf("PDF must be a regular file no larger than %d bytes", limits.FileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var header [5]byte
	if _, err := io.ReadFull(f, header[:]); err != nil || string(header[:]) != "%PDF-" {
		return "", fmt.Errorf("not a PDF: %s", path)
	}
	return path, nil
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		err = run(o, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zat:", err)
		os.Exit(1)
	}
}
