// goiv is a keyboard and pointer-driven image viewer for st-go's retained
// raster region.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type options struct{ file string }

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("goiv", flag.ContinueOnError)
	f.SetOutput(out)
	f.Usage = func() {
		fmt.Fprintln(out, "Usage: goiv image\n\nw fit width; a fit height; o original size; n/p next/previous; Ctrl+wheel or Ctrl+/- zoom; drag to pan; q quit.")
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 1 {
		return o, errors.New("one image filename is required")
	}
	path, err := filepath.Abs(f.Arg(0))
	if err != nil {
		return o, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return o, err
	}
	if !info.Mode().IsRegular() || !isImageName(path) {
		return o, fmt.Errorf("not a supported image: %s", filepath.Base(path))
	}
	if !protocolPath(path) {
		return o, fmt.Errorf("image path contains terminal control characters")
	}
	o.file = path
	return o, nil
}

func protocolPath(path string) bool {
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func isImageName(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff", ".webp":
		return true
	}
	return false
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if err == flag.ErrHelp {
		return
	}
	if err == nil {
		err = run(o, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "goiv:", err)
		os.Exit(1)
	}
}
