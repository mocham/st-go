package term

import (
	"fmt"
	"strconv"
	"strings"
)

// RegionID is a stable display target, independent of its current position.
type RegionID string

const (
	MainRegion      RegionID = "main"
	SecondaryRegion RegionID = "secondary"
)

// CellRect uses zero-based cell coordinates and exclusive width/height extents.
// The DCS interface uses one-based X/Y, like the existing open rect command.
type CellRect struct {
	X, Y, Width, Height int
}

type DisplayRegion struct {
	ID     RegionID
	Bounds CellRect
}

// RegionImage replaces the target's retained image. Page is zero-based in
// Options. The initial implementation supports static images and PDF pages.
type RegionImage struct {
	Path    string
	Options ImageDecodeOptions
	// KeepPage retains the target's current page when replacing a document.
	KeepPage bool
	// Continuous displays PDF pages as one locally scrollable, fit-width view.
	Continuous bool
	// Background is an opaque 0xAARRGGBB paper/canvas color for continuous
	// PDF views. Zero keeps the default white background.
	Background uint32
}

// RegionHooks is an optional frontend capability. Keeping rectangle-based
// layouts and explicit targets allows more layouts without changing callers.
// Implementations validate the whole layout before applying it. nil restores
// the full-window main region. OpenRegion queues a replacement; the previous
// content remains visible until decoding succeeds. Calls are serialized with
// terminal output by the frontend, like Hooks.
type RegionHooks interface {
	SetRegions([]DisplayRegion) error
	Regions() []DisplayRegion
	OpenRegion(RegionID, RegionImage) error
	ClearRegion(RegionID) error
}

// RegionNavigation describes an application-owned preview position. scroll
// values are signed wheel steps; seek values are one-based source lines.
// position is a local continuous-view position in millionths (0..1,000,000).
type RegionNavigation struct {
	Kind  string
	Value int
}

// RegionNavigationHooks is optional, keeping the base region API compatible.
type RegionNavigationHooks interface {
	SetRegionScrollReporting(RegionID, bool) error
	NavigateRegion(RegionID, RegionNavigation) error
}

func (t *Term) dslRegion(args []string) error {
	hooks, ok := t.hooks.(RegionHooks)
	if !ok {
		return fmt.Errorf("display regions are not supported by this frontend")
	}
	if len(args) == 0 {
		return fmt.Errorf("region requires layout, split, reset, query, or a target")
	}
	switch args[0] {
	case "reset":
		if len(args) != 1 {
			return fmt.Errorf("region reset takes no arguments")
		}
		return hooks.SetRegions(nil)
	case "query":
		if len(args) != 1 {
			return fmt.Errorf("region query takes no arguments")
		}
		var reply strings.Builder
		reply.WriteString("\x1bPregion layout")
		for _, r := range hooks.Regions() {
			b := r.Bounds
			fmt.Fprintf(&reply, " %s %d %d %d %d", r.ID, b.X+1, b.Y+1, b.Width, b.Height)
		}
		reply.WriteString("\x1b\\")
		t.WriteToTTY([]byte(reply.String()), false)
		return nil
	case "layout":
		if len(args) < 6 || (len(args)-1)%5 != 0 || len(args) > 11 {
			return fmt.Errorf("region layout requires one or two ID X Y W H rectangles")
		}
		var regions []DisplayRegion
		for i := 1; i < len(args); i += 5 {
			var n [4]int
			for j := range n {
				v, err := strconv.Atoi(args[i+1+j])
				if err != nil || v < 1 {
					return fmt.Errorf("invalid region rectangle value %q", args[i+1+j])
				}
				n[j] = v
			}
			regions = append(regions, DisplayRegion{RegionID(args[i]), CellRect{n[0] - 1, n[1] - 1, n[2], n[3]}})
		}
		return hooks.SetRegions(regions)
	case "split":
		// Convenience syntax; translate into the same rectangle-based API.
		if len(args) != 2 {
			return fmt.Errorf("region split requires the secondary width in columns or percent")
		}
		cols, rows := 0, 0
		for _, r := range hooks.Regions() {
			cols = max(cols, r.Bounds.X+r.Bounds.Width)
			rows = max(rows, r.Bounds.Y+r.Bounds.Height)
		}
		value := strings.TrimSuffix(args[1], "%")
		width, err := strconv.Atoi(value)
		if err != nil || width < 1 {
			return fmt.Errorf("invalid split width %q", args[1])
		}
		if value != args[1] {
			if width >= 100 {
				return fmt.Errorf("split percentage must be between 1 and 99")
			}
			width = max(1, cols*width/100)
		}
		if width >= cols {
			return fmt.Errorf("split must leave at least one column in each region")
		}
		return hooks.SetRegions([]DisplayRegion{
			{MainRegion, CellRect{0, 0, cols - width, rows}},
			{SecondaryRegion, CellRect{cols - width, 0, width, rows}},
		})
	}
	if len(args) < 2 {
		return fmt.Errorf("region target requires open or clear")
	}
	id := RegionID(args[0])
	switch args[1] {
	case "scroll", "seek", "position":
		if len(args) != 3 {
			return fmt.Errorf("region navigation requires one value")
		}
		navigation, ok := t.hooks.(RegionNavigationHooks)
		if !ok {
			return fmt.Errorf("region navigation is not supported by this frontend")
		}
		if args[1] == "scroll" && (args[2] == "report" || args[2] == "pages") {
			return navigation.SetRegionScrollReporting(id, args[2] == "report")
		}
		value, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("invalid navigation value %q", args[2])
		}
		return navigation.NavigateRegion(id, RegionNavigation{Kind: args[1], Value: value})
	case "clear":
		if len(args) != 2 {
			return fmt.Errorf("region clear takes no arguments")
		}
		return hooks.ClearRegion(id)
	case "open":
		if len(args) < 3 {
			return fmt.Errorf("region open requires a path")
		}
		req := RegionImage{Path: t.resolveImagePath(args[2]), Options: ImageDecodeOptions{FitContain: true}}
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "background":
				if i+1 >= len(args) || len(args[i+1]) != 7 || args[i+1][0] != '#' {
					return fmt.Errorf("region background requires a #RRGGBB color")
				}
				value, err := strconv.ParseUint(args[i+1][1:], 16, 24)
				if err != nil {
					return fmt.Errorf("invalid region background color %q", args[i+1])
				}
				req.Background = 0xff000000 | uint32(value)
				i++
			case "continuous":
				req.Continuous = true
			case "fit-contain", "fit-width", "fit-height", "native":
				req.Options.FitContain = args[i] == "fit-contain"
				req.Options.FitWidth = args[i] == "fit-width"
				req.Options.FitHeight = args[i] == "fit-height"
			case "page":
				if i+1 >= len(args) {
					return fmt.Errorf("region open page requires a positive page number")
				}
				if args[i+1] == "current" {
					req.KeepPage = true
					i++
					continue
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 1 {
					return fmt.Errorf("invalid region page %q", args[i+1])
				}
				req.Options.Page = n - 1
				req.KeepPage = false
				i++
			default:
				return fmt.Errorf("unsupported region open option %q", args[i])
			}
		}
		if req.Background != 0 && !req.Continuous {
			return fmt.Errorf("region background requires a continuous PDF view")
		}
		return hooks.OpenRegion(id, req)
	default:
		return fmt.Errorf("unknown region operation %q", args[1])
	}
}
