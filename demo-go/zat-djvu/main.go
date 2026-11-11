// zat-djvu is a keyboard-driven DjVu viewer using DjVuLibre and st-go's
// retained raster region.
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
	backgroundRGB    uint32
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var options options
	flags := flag.NewFlagSet("zat-djvu", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.IntVar(&options.page, "page", 1, "initial page (one-based)")
	flags.IntVar(&options.zoom, "zoom", 100, "zoom relative to fit-width (10..500 percent)")
	flags.StringVar(&options.background, "bg", "#f7f5ef", "DjVu output whitepoint (#RRGGBB)")
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: zat-djvu [options] document.djvu\n\nRun inside st-go. Tab: outline; Shift+Tab: selectable page text.\nj/k scroll, J/K change page, gg/G jump, +/- zoom, :help, q quit.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 1 {
		return options, fmt.Errorf("one DjVu filename is required")
	}
	if options.page < 1 || options.page > limits.PreviewPages {
		return options, fmt.Errorf("-page must be 1..%d", limits.PreviewPages)
	}
	if options.zoom < 10 || options.zoom > 500 {
		return options, fmt.Errorf("-zoom must be 10..500")
	}
	if len(options.background) != 7 || options.background[0] != '#' {
		return options, fmt.Errorf("-bg must be #RRGGBB")
	}
	background, err := strconv.ParseUint(options.background[1:], 16, 24)
	if err != nil {
		return options, fmt.Errorf("-bg must be #RRGGBB")
	}
	options.backgroundRGB = uint32(background)
	options.file, err = validateDjVu(flags.Arg(0))
	return options, err
}

func validateDjVu(path string) (string, error) {
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
	for _, character := range path {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("DjVu filename contains a control character")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > limits.FileBytes {
		return "", fmt.Errorf("DjVu index must be a regular file no larger than %d bytes", limits.FileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var header [8]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[:]) != "AT&TFORM" {
		return "", fmt.Errorf("not a DjVu file: %s", path)
	}
	return path, nil
}

func main() {
	options, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		err = run(options, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zat-djvu:", err)
		os.Exit(1)
	}
}
