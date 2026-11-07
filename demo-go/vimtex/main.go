// vimtex runs real Vim beside st-go's retained PDF preview region.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const minimumGap = 2 * time.Second

type options struct {
	file         string
	vim          string
	vimArgs      []string
	previewWidth int
	gap          time.Duration
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var o options
	var split string
	flags := flag.NewFlagSet("vimtex", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&o.vim, "vim", "vim", "Vim executable")
	flags.StringVar(&split, "split", "4:6", "Vim:preview width ratio (positive integers)")
	flags.IntVar(&o.previewWidth, "width", 60, "right preview width, percent (1–99; alternative to -split)")
	flags.DurationVar(&o.gap, "gap", 2100*time.Millisecond, "minimum file-refresh gap (must be greater than 2s)")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: vimtex [options] existing.tex [vim-options...]\n\nRun inside st-go. Save to refresh; wheel scrolls the preview smoothly.\n:VimtexPreview [line] and :VimtexScroll count navigate explicitly from Vim.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() < 1 {
		return o, fmt.Errorf("an existing LaTeX file is required")
	}
	if o.gap <= minimumGap {
		return o, fmt.Errorf("-gap must be greater than 2s")
	}
	var splitSet, widthSet bool
	flags.Visit(func(f *flag.Flag) {
		splitSet = splitSet || f.Name == "split"
		widthSet = widthSet || f.Name == "width"
	})
	if splitSet && widthSet {
		return o, fmt.Errorf("use either -split or -width, not both")
	}
	if !widthSet {
		left, right, found := strings.Cut(split, ":")
		vimWidth, leftErr := strconv.ParseUint(strings.TrimSpace(left), 10, 32)
		previewWidth, rightErr := strconv.ParseUint(strings.TrimSpace(right), 10, 32)
		if !found || leftErr != nil || rightErr != nil || vimWidth == 0 || previewWidth == 0 {
			return o, fmt.Errorf("-split must be a positive integer Vim:preview ratio, such as 4:6")
		}
		// The region protocol uses whole percentages. Bounded uint32 inputs
		// keep this uint64 arithmetic safe even for large ratio components.
		total := vimWidth + previewWidth
		o.previewWidth = int((100*previewWidth + total/2) / total)
		if o.previewWidth < 1 || o.previewWidth > 99 {
			return o, fmt.Errorf("-split must round to a preview width between 1%% and 99%%")
		}
	}
	if o.previewWidth < 1 || o.previewWidth > 99 {
		return o, fmt.Errorf("-width must be between 1 and 99")
	}
	path, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return o, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return o, fmt.Errorf("open source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return o, fmt.Errorf("source must be an existing regular file: %s", path)
	}
	o.file, o.vimArgs = path, flags.Args()[1:]
	if o.vim, err = exec.LookPath(o.vim); err != nil {
		return o, err
	}
	return o, nil
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		// Runtime diagnostics go through limits.Report's bounded /tmp log.
		// Standard logger notices must not interleave with Vim's output.
		log.SetOutput(io.Discard)
		err = run(o, os.Stdin, os.Stdout)
	}
	if err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			code := exited.ExitCode()
			if code < 0 {
				code = 1
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "vimtex:", err)
		os.Exit(1)
	}
}
