package main

import (
	"fmt"
	"image"
	"math"
	"sort"
	"sync"
	"time"
	"unicode"

	"st-go/limits"
	"st-go/term"
)

// The public interface is rectangle-based; this backend currently supports
// adjoining main-left/secondary-right or main-top/secondary-bottom regions.
// Layout belongs to Terminal.mu. Preview content, navigation and jobs belong
// to mu. Lock order is Terminal.mu -> mu; preview workers never take Terminal.mu.
// Geometry is copied at layout changes so workers never read the terminal grid.
type displayRegionState struct {
	mu                    sync.Mutex
	mainWidth, totalWidth int // requested ratio, preserved across WM resizes
	mainRows, windowRows  int // nonzero mainRows selects a stacked layout
	secondary             *regionSurface
	generation            uint64
	documentRevision      uint64
	jobs                  chan regionJob // one pending job, latest request wins
	fullDirty             bool
	pane, viewport        image.Rectangle
	layoutEpoch           uint64
	background            uint32
	cellWidth, cellHeight int
	paused                bool
	wake                  chan struct{}
	replies               chan regionReply
	presenter             regionPresenter
}

type regionReply struct {
	generation uint64
	text       string
}

type regionSurface struct {
	request               *term.RegionImage
	bitmap                regionBitmap
	dirty                 bool
	reportScroll          bool
	loading, scrollFailed bool
	scrollY, targetY      float64
	lastScroll            time.Time
	viewWidth             int
	seekPosition          *int
	seekPage              *int
	zoom, pan             int // zoom percent of fit-width; zero means 100
}

type regionBitmap struct {
	pixels             []uint32
	width, height      int
	page               int // resolved zero-based page, including modulo wrapping
	top, contentHeight int // cached strip origin and full continuous height
	background         uint32
	pageTops           []int // one offset per page plus the document end
}

type regionJob struct {
	generation    uint64
	revision      uint64
	request       term.RegionImage
	width, height int
	background    uint32
	position      int
	search        *regionSearch
}

type regionSearch struct {
	text            string
	page, direction int
}

var _ term.RegionHooks = (*Terminal)(nil)

func (t *Terminal) physicalCols() int {
	if t.windowCols > 0 {
		return t.windowCols
	}
	return t.cols
}

func (t *Terminal) physicalRows() int {
	if t.displayRegions.windowRows > 0 {
		return t.displayRegions.windowRows
	}
	return t.rows
}

func (t *Terminal) Regions() []term.DisplayRegion {
	regions := []term.DisplayRegion{{ID: term.MainRegion, Bounds: term.CellRect{Width: t.cols, Height: t.rows}}}
	if t.displayRegions.secondary != nil {
		bounds := term.CellRect{X: t.cols, Width: t.physicalCols() - t.cols, Height: t.rows}
		if t.displayRegions.mainRows > 0 {
			bounds = term.CellRect{Y: t.rows, Width: t.cols, Height: t.physicalRows() - t.rows}
		}
		regions = append(regions, term.DisplayRegion{ID: term.SecondaryRegion, Bounds: bounds})
	}
	return regions
}

func validateDisplayLayout(regions []term.DisplayRegion, cols, rows int) (int, error) {
	if len(regions) == 0 {
		return cols, nil
	}
	if len(regions) > 2 {
		return 0, fmt.Errorf("only two adjoining display regions are supported")
	}
	var main, secondary term.CellRect
	seen := make(map[term.RegionID]bool)
	for _, r := range regions {
		if seen[r.ID] {
			return 0, fmt.Errorf("duplicate display region %q", r.ID)
		}
		seen[r.ID] = true
		b := r.Bounds
		// Compare before adding coordinates, avoiding integer overflow.
		if b.X < 0 || b.X >= cols || b.Y < 0 || b.Y >= rows || b.Width < 1 || b.Width > cols-b.X || b.Height < 1 || b.Height > rows-b.Y {
			return 0, fmt.Errorf("regions must be in-bounds and positive")
		}
		switch r.ID {
		case term.MainRegion:
			main = b
		case term.SecondaryRegion:
			secondary = b
		default:
			return 0, fmt.Errorf("unsupported display region %q", r.ID)
		}
	}
	if !seen[term.MainRegion] || main.X != 0 || main.Y != 0 {
		return 0, fmt.Errorf("main must start at the top left")
	}
	if len(regions) == 1 {
		if main.Width != cols || main.Height != rows {
			return 0, fmt.Errorf("a single main region must fill the window")
		}
	} else if main.Width == cols && secondary.X == 0 && secondary.Y == main.Height && secondary.Width == cols && secondary.Height == rows-main.Height {
		return -main.Height, nil // stacked: reserve a fixed main height
	} else if main.Height != rows || secondary.Y != 0 || secondary.Height != rows || secondary.X != main.Width || secondary.Width != cols-main.Width {
		return 0, fmt.Errorf("secondary must adjoin main and fill the remaining window")
	}
	return main.Width, nil
}

func (t *Terminal) SetRegions(regions []term.DisplayRegion) error {
	if t.termCore == nil || t.cw < 1 || t.ch < 1 {
		return fmt.Errorf("terminal display is not initialized")
	}
	t.pauseRegionPaint(t.termCore.IsPaintPaused())
	cols := t.physicalCols()
	rows := t.physicalRows()
	mainWidth, err := validateDisplayLayout(regions, cols, rows)
	if err != nil {
		return err
	}
	s := &t.displayRegions
	s.mu.Lock()
	if mainWidth == cols {
		s.generation++ // invalidate both pending and in-flight decode results
		s.secondary = nil
		t.cancelRegionDecode()
		s.mainWidth, s.totalWidth = 0, 0
		s.mainRows = 0
	} else {
		s.mainWidth, s.totalWidth = mainWidth, cols
		s.mainRows = max(0, -mainWidth)
		if s.secondary == nil {
			s.secondary = &regionSurface{dirty: true}
		}
	}
	s.mu.Unlock()
	t.applyDisplayLayout(cols, rows)
	return nil
}

// applyDisplayLayout also runs after a physical resize. At a one-column window
// the preview is temporarily hidden; the requested ratio and content survive.
func (t *Terminal) applyDisplayLayout(cols, rows int) {
	s := &t.displayRegions
	s.mu.Lock()
	mainCols, mainRows := cols, rows
	if s.secondary != nil {
		if s.mainRows > 0 {
			mainRows = min(s.mainRows, max(1, rows-1))
		} else if cols > 1 {
			mainCols = clampInt(int(int64(cols)*int64(s.mainWidth)/int64(s.totalWidth)), 1, cols-1)
		}
	}
	changed := mainCols != t.cols || mainRows != t.rows
	t.windowCols, s.windowRows, t.cols, t.rows = cols, rows, mainCols, mainRows
	s.pane, s.viewport = t.secondaryPanePixels(), t.secondaryPixels()
	s.layoutEpoch++
	s.background = t.colorAt(ColDefaultBg)
	s.cellWidth, s.cellHeight = t.cw, t.ch
	s.fullDirty = true
	if s.secondary != nil {
		s.secondary.dirty = true
		t.queueRegionImage()
	}
	s.mu.Unlock()
	t.configureRegionPresenter()
	if t.termCore != nil {
		if t.termCore.Cols() != mainCols || t.termCore.Rows() != mainRows {
			t.termCore.Tresize(mainCols, mainRows)
		}
		if changed && t.ttyResize != nil {
			t.ttyResize(mainRows, mainCols)
		}
		t.termCore.Redraw()
	}
}

func (t *Terminal) secondaryRegion(id term.RegionID) (*regionSurface, error) {
	if id != term.SecondaryRegion || t.displayRegions.secondary == nil {
		return nil, fmt.Errorf("region %q is not an active preview target", id)
	}
	return t.displayRegions.secondary, nil
}

func (t *Terminal) OpenRegion(id term.RegionID, request term.RegionImage) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	return t.openRegion(id, request)
}

func (t *Terminal) openRegion(id term.RegionID, request term.RegionImage) error {
	if t.termCore != nil {
		t.displayRegions.paused = t.termCore.IsPaintPaused()
	}
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if request.Path == "" || request.Options.Animate || request.Options.ViewCols != 0 || request.Options.ViewRows != 0 {
		return fmt.Errorf("region open requires a static image/PDF filling the region")
	}
	if !limits.Check("region path", int64(len(request.Path)), limits.ControlBytes) {
		return fmt.Errorf("region path exceeds resource limit")
	}
	if request.Background != 0 && (!request.Continuous || request.Background>>24 != 255) {
		return fmt.Errorf("region background requires an opaque color and a continuous PDF view")
	}
	if request.KeepPage && surface.request != nil {
		request.Options.Page = surface.request.Options.Page
	}
	initialPage := request.Continuous && request.Options.Page > 0 && !request.KeepPage
	request.KeepPage = false
	if !request.Continuous || surface.request == nil || !surface.request.Continuous {
		surface.scrollY, surface.targetY = 0, 0
		surface.seekPosition = nil
		surface.seekPage = nil
	}
	surface.request = &request
	t.displayRegions.documentRevision++
	if initialPage {
		page := request.Options.Page + 1
		surface.seekPage = &page
	}
	t.queueRegionImage()
	if len(surface.bitmap.pixels) == 0 {
		surface.dirty = true
		t.requestRegionPaint()
	}
	return nil
}

func (t *Terminal) ClearRegion(id term.RegionID) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	if t.termCore != nil {
		t.displayRegions.paused = t.termCore.IsPaintPaused()
	}
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	t.displayRegions.generation++
	t.cancelRegionDecode()
	surface.request = nil
	surface.bitmap = regionBitmap{}
	surface.scrollY, surface.targetY = 0, 0
	surface.seekPosition = nil
	surface.seekPage = nil
	surface.loading, surface.scrollFailed = false, false
	surface.dirty = true
	t.requestRegionPaint()
	return nil
}

func (t *Terminal) queueRegionImage() {
	s := &t.displayRegions
	s.generation++
	surface := s.secondary
	if surface == nil || surface.request == nil {
		t.cancelRegionDecode()
		return
	}
	r := s.viewport
	if r.Empty() {
		return
	}
	width := surface.renderWidth(r.Dx())
	if surface.bitmap.contentHeight > 0 && surface.viewWidth > 0 && surface.viewWidth != width {
		// Keep the same document coordinate when the fit-width scale changes.
		scale := float64(width) / float64(surface.viewWidth)
		surface.scrollY *= scale
		surface.targetY *= scale
	}
	surface.viewWidth = width
	surface.pan = clampInt(surface.pan, 0, max(0, width-r.Dx()))
	surface.scrollFailed = false
	t.enqueueRegionJob(regionJob{generation: s.generation, revision: s.documentRevision, request: *surface.request,
		width: width, height: r.Dy(), background: surface.background(s.background), position: int(surface.scrollY)})
}

func (s *regionSurface) zoomPercent() int {
	if s.zoom == 0 {
		return 100
	}
	return s.zoom
}

func (s *regionSurface) renderWidth(width int) int {
	if s.request == nil || !s.request.Continuous {
		return width
	}
	return max(1, width*s.zoomPercent()/100)
}

func (s *regionSurface) background(fallback uint32) uint32 {
	if s.request != nil && s.request.Continuous {
		if s.request.Background != 0 {
			return s.request.Background
		}
		return 0xffffffff
	}
	return fallback
}

func (t *Terminal) cancelRegionDecode() {
	if t.displayRegions.jobs != nil {
		t.enqueueRegionJob(regionJob{generation: t.displayRegions.generation})
	}
}

func (t *Terminal) enqueueRegionJob(job regionJob) {
	s := &t.displayRegions
	if s.jobs == nil {
		s.jobs = make(chan regionJob, 1)
		if t.done == nil {
			t.done = make(chan struct{})
		}
		go t.regionDecoder(s.jobs, t.done)
	}
	if s.secondary != nil {
		s.secondary.loading = job.request.Path != ""
	}
	// The worker may take a queued job between these operations. This producer
	// is serialized by the preview mutex, so the final send always has room.
	select {
	case <-s.jobs:
	default:
	}
	s.jobs <- job
}

func (t *Terminal) regionDecoder(jobs <-chan regionJob, done <-chan struct{}) {
	var document *scrollPDF
	var documentRevision uint64
	defer func() { document.close() }()
	for {
		select {
		case <-done:
			return
		case job := <-jobs:
			select {
			case <-done:
				return
			default:
			}
			t.displayRegions.mu.Lock()
			current := job.generation == t.displayRegions.generation
			t.displayRegions.mu.Unlock()
			if !current {
				continue
			}
			if job.request.Path == "" {
				document.close()
				document = nil
				continue
			}
			var bitmap regionBitmap
			var err error
			found := 0
			visibleFirst := false
			if job.request.Continuous {
				if document == nil || documentRevision != job.revision {
					var data []byte
					data, err = limits.ReadFile(job.request.Path)
					if err == nil {
						var next *scrollPDF
						next, err = openScrollPDF(data)
						if err == nil {
							document.close()
							document, documentRevision = next, job.revision
						}
					}
				}
				if err == nil {
					if job.search != nil {
						found, job.position, err = document.find(job.width, job.search, func() bool {
							t.displayRegions.mu.Lock()
							defer t.displayRegions.mu.Unlock()
							return !t.closed() && job.generation == t.displayRegions.generation
						})
					}
					if err == nil && (job.search == nil || found > 0) {
						// Publish the visible area first on open/zoom/jump, then
						// populate the larger surrounding cache in the background.
						b := document.cache
						visibleFirst = b.width != job.width || b.background != job.background ||
							job.position < b.top || job.position+job.height > b.top+b.height
						screens := limits.PreviewScreens
						if visibleFirst {
							screens = 1
						}
						bitmap, err = document.renderScreens(job.width, job.height, job.position, job.background, screens)
					}
				}
			} else {
				document.close()
				document = nil
				bitmap, err = renderRegionImage(job)
			}
			t.displayRegions.mu.Lock()
			if job.search != nil && !t.closed() && job.generation == t.displayRegions.generation && t.displayRegions.secondary != nil {
				s := t.displayRegions.secondary
				s.loading = false
				if err == nil && found > 0 {
					s.scrollY, s.targetY = float64(job.position), float64(job.position)
				}
				t.queueRegionReply(regionReply{job.generation, fmt.Sprintf("\x1bPregion secondary found %d\x1b\\", found)})
			}
			if job.search != nil && found == 0 && err == nil {
				t.queueRegionScroll()
				t.displayRegions.mu.Unlock()
				continue
			}
			t.acceptRegionImage(job, bitmap, err)
			if visibleFirst && err == nil && !t.closed() && job.generation == t.displayRegions.generation {
				s := t.displayRegions.secondary
				if s != nil && !s.loading && bitmap.height < min(bitmap.contentHeight, limits.PreviewScreens*job.height, limits.PreviewCacheBytes/4/job.width) {
					job.search = nil
					job.position = int(s.scrollY)
					t.enqueueRegionJob(job)
				}
			}
			t.displayRegions.mu.Unlock()
		}
	}
}

func (t *Terminal) acceptRegionImage(job regionJob, bitmap regionBitmap, err error) {
	if t.closed() || t.displayRegions.secondary == nil || job.generation != t.displayRegions.generation {
		return
	}
	surface := t.displayRegions.secondary
	surface.loading = false
	if err != nil {
		surface.scrollFailed = true
		limits.Report("preview region", "%s: %v", job.request.Path, err)
		return
	}
	surface.scrollFailed = false
	surface.bitmap = bitmap
	if bitmap.contentHeight > 0 {
		maximum := float64(max(0, bitmap.contentHeight-t.displayRegions.viewport.Dy()))
		if surface.seekPosition != nil {
			surface.scrollY = maximum * float64(*surface.seekPosition) / 1e6
			surface.targetY = surface.scrollY
			surface.seekPosition = nil
		}
		if surface.seekPage != nil && len(bitmap.pageTops) > 1 {
			page := clampInt(*surface.seekPage, 1, len(bitmap.pageTops)-1)
			surface.scrollY = float64(bitmap.pageTops[page-1])
			surface.targetY = surface.scrollY
			surface.seekPage = nil
		}
		surface.scrollY = max(0, min(surface.scrollY, maximum))
		surface.targetY = max(0, min(surface.targetY, maximum))
		t.queueRegionScroll()
	}
	if request := t.displayRegions.secondary.request; request != nil && !request.Continuous {
		request.Options.Page = bitmap.page
	}
	t.displayRegions.secondary.dirty = true
	t.requestRegionPaint()
}

func (t *Terminal) requestRegionPaint() {
	if wake := t.displayRegions.wake; wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (t *Terminal) terminalClip() image.Rectangle {
	r := image.Rect(0, 0, fbW, fbH)
	if t.displayRegions.secondary != nil {
		r = r.Intersect(image.Rect(t.borderpx, t.borderpx, t.borderpx+t.cols*t.cw, t.borderpx+t.rows*t.ch))
	}
	return r
}

func (t *Terminal) secondaryPixels() image.Rectangle {
	r := t.secondaryPanePixels()
	if t.displayRegions.mainRows == 0 {
		r.Min.X += min(3, max(0, r.Dx()-1))
	}
	return r
}

func (t *Terminal) secondaryPanePixels() image.Rectangle {
	if t.displayRegions.mainRows > 0 {
		return image.Rect(t.borderpx, t.borderpx+t.rows*t.ch, t.borderpx+t.cols*t.cw, t.borderpx+t.physicalRows()*t.ch).Intersect(image.Rect(0, 0, fbW, fbH))
	}
	return image.Rect(t.borderpx+t.cols*t.cw, t.borderpx, t.borderpx+t.physicalCols()*t.cw, t.borderpx+t.rows*t.ch).Intersect(image.Rect(0, 0, fbW, fbH))
}

func (t *Terminal) overSecondary(px, py int) bool {
	return t.displayRegions.secondary != nil && image.Pt(px, py).In(t.secondaryPanePixels())
}

func (t *Terminal) regionMousePress(button byte, px, py int) bool {
	if buttons != 0 || !t.overSecondary(px, py) {
		return false // a main-region drag keeps ownership through its release
	}
	t.regionButtons |= buttonmask(button)
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	surface := t.displayRegions.secondary
	if button == 4 || button == 5 {
		delta := 1
		if button == 4 {
			delta = -1
		}
		if surface.request != nil && surface.request.Continuous {
			t.scrollRegionBy(delta)
			return true
		}
		if surface.reportScroll {
			_ = t.navigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "scroll", Value: delta})
			return true
		}
		if surface.request == nil {
			return true
		}
		req := *surface.request
		req.Options.Page += delta
		_ = t.openRegion(term.SecondaryRegion, req)
	}
	return true
}

func (t *Terminal) SetRegionScrollReporting(id term.RegionID, enabled bool) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	surface, err := t.secondaryRegion(id)
	if err == nil {
		surface.reportScroll = enabled
	}
	return err
}

func (t *Terminal) FindRegion(id term.RegionID, text string, direction int, next bool) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	if t.termCore != nil {
		t.displayRegions.paused = t.termCore.IsPaintPaused()
	}
	s, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if s.request == nil || !s.request.Continuous || len(s.bitmap.pageTops) < 2 {
		return fmt.Errorf("search requires a loaded continuous PDF")
	}
	if text == "" || len(text) > limits.PreviewSearchBytes || (direction != 1 && direction != -1) {
		return fmt.Errorf("invalid PDF search")
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf("PDF search contains control characters")
		}
	}
	page := s.currentPage() - 1
	if next {
		page += direction
	}
	s.seekPage, s.seekPosition = nil, nil
	t.displayRegions.generation++
	r := t.secondaryPixels()
	t.enqueueRegionJob(regionJob{generation: t.displayRegions.generation, revision: t.displayRegions.documentRevision,
		request: *s.request, width: s.renderWidth(r.Dx()), height: r.Dy(), background: s.background(t.colorAt(ColDefaultBg)),
		search: &regionSearch{text: text, page: page, direction: direction}})
	return nil
}

func (t *Terminal) NavigateRegion(id term.RegionID, event term.RegionNavigation) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	return t.navigateRegion(id, event)
}

func (t *Terminal) navigateRegion(id term.RegionID, event term.RegionNavigation) error {
	if t.termCore != nil {
		t.displayRegions.paused = t.termCore.IsPaintPaused()
	}
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if event.Value < -limits.MaxBytes || event.Value > limits.MaxBytes || (event.Kind == "seek" && event.Value < 1) {
		return fmt.Errorf("invalid region navigation event")
	}
	if event.Kind == "info" {
		page, pages := surface.currentPage(), max(0, len(surface.bitmap.pageTops)-1)
		position := int(surface.scrollY / max(1, t.regionScrollMaximum()) * 1e6)
		loading, failed := 0, 0
		if surface.loading && !surface.covers(t.displayRegions.viewport.Dy()) {
			loading = 1
		}
		if surface.scrollFailed {
			failed = 1
		}
		t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion %s status %d %d %d %d %d %d\x1b\\", id, page, pages, clampInt(position, 0, 1000000), surface.zoomPercent(), loading, failed)), false)
		return nil
	}
	switch event.Kind {
	case "page", "pages", "view", "zoom", "pan":
		if surface.request == nil || !surface.request.Continuous {
			return fmt.Errorf("navigation requires a continuous PDF")
		}
		switch event.Kind {
		case "zoom":
			// 0 is best-fit for the current page; 100 is fit-width.
			zoom := event.Value
			if zoom == 0 && len(surface.bitmap.pageTops) > 1 {
				page := surface.currentPage() - 1
				height := surface.bitmap.pageTops[page+1] - surface.bitmap.pageTops[page]
				zoom = min(100, max(10, t.secondaryPixels().Dy()*surface.zoomPercent()/max(1, height)))
			}
			if zoom < 10 || zoom > 500 {
				return fmt.Errorf("zoom must be 10..500 percent, or 0 for best-fit")
			}
			if zoom != surface.zoomPercent() {
				surface.zoom = zoom
				t.queueRegionImage()
			}
		case "pan":
			surface.pan = clampInt(surface.pan+event.Value*max(24, 3*t.displayRegions.cellWidth), 0, max(0, surface.viewWidth-t.secondaryPixels().Dx()))
			surface.dirty = true
			t.requestRegionPaint()
		case "view":
			t.scrollRegionPixels(float64(event.Value) * float64(t.secondaryPixels().Dy()) / 1e6)
		case "page", "pages":
			page := event.Value
			if event.Kind == "pages" {
				page += surface.currentPage()
			}
			if page < 1 {
				page = 1
			}
			if surface.loading || len(surface.bitmap.pageTops) < 2 {
				surface.seekPage = &page
				surface.seekPosition = nil
			} else {
				page = min(page, len(surface.bitmap.pageTops)-1)
				surface.scrollY = min(float64(surface.bitmap.pageTops[page-1]), t.regionScrollMaximum())
				surface.targetY = surface.scrollY
				surface.dirty = true
				t.queueRegionScroll()
				t.requestRegionPaint()
			}
		}
		return nil
	}
	if event.Kind == "position" {
		if event.Value < 0 || event.Value > 1000000 || surface.request == nil || !surface.request.Continuous {
			return fmt.Errorf("position requires a continuous PDF and a value from 0 to 1000000")
		}
		if surface.loading {
			value := event.Value
			surface.seekPosition = &value
		}
		surface.seekPage = nil
		maximum := t.regionScrollMaximum()
		surface.scrollY = maximum * float64(event.Value) / 1e6
		surface.targetY = surface.scrollY
		surface.dirty = true
		t.queueRegionScroll()
		t.requestRegionPaint()
		return nil
	}
	if event.Kind == "scroll" && surface.request != nil && surface.request.Continuous {
		t.scrollRegionBy(event.Value)
		return nil
	}
	if (event.Kind != "seek" && event.Kind != "scroll") || !surface.reportScroll {
		return fmt.Errorf("region navigation events are not enabled")
	}
	t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion %s event %s %d\x1b\\", id, event.Kind, event.Value)), false)
	return nil
}

func (s *regionSurface) currentPage() int {
	if len(s.bitmap.pageTops) < 2 {
		return 0
	}
	y := int(s.scrollY * float64(s.bitmap.width) / float64(max(1, s.viewWidth)))
	return clampInt(sort.SearchInts(s.bitmap.pageTops, y+1), 1, len(s.bitmap.pageTops)-1)
}

func (t *Terminal) scrollRegionBy(steps int) {
	t.scrollRegionPixels(float64(steps) * float64(max(24, 3*t.displayRegions.cellHeight)))
}

func (t *Terminal) scrollRegionPixels(pixels float64) {
	s := t.displayRegions.secondary
	maximum := t.regionScrollMaximum()
	s.targetY = max(0, min(s.targetY+pixels, maximum))
	// Give the input event an immediate first frame, then ease at display rate.
	now := time.Now()
	s.lastScroll = now.Add(-16 * time.Millisecond)
	t.tickRegionScroll(now)
}

func (t *Terminal) regionScrollMaximum() float64 {
	b := t.displayRegions.secondary.bitmap
	r := t.displayRegions.viewport
	return max(0, float64(b.contentHeight)*float64(t.displayRegions.secondary.renderWidth(r.Dx()))/float64(max(1, b.width))-float64(r.Dy()))
}

func (t *Terminal) tickRegionScroll(now time.Time) {
	s := t.displayRegions.secondary
	if s == nil || s.request == nil || !s.request.Continuous || s.scrollY == s.targetY {
		return
	}
	dt := max(0, min(now.Sub(s.lastScroll).Seconds(), 0.05))
	s.lastScroll = now
	s.scrollY += (s.targetY - s.scrollY) * (1 - math.Exp(-dt/0.045))
	if math.Abs(s.targetY-s.scrollY) < 0.5 {
		s.scrollY = s.targetY
	}
	s.dirty = true
	t.queueRegionScroll()
	t.requestRegionPaint()
}

func (t *Terminal) queueRegionScroll() {
	s := t.displayRegions.secondary
	if s == nil || s.request == nil || !s.request.Continuous || s.loading || s.scrollFailed || s.bitmap.contentHeight == 0 {
		return
	}
	r := t.displayRegions.viewport
	if r.Empty() {
		return
	}
	b := s.bitmap
	position := int(s.scrollY + max(-float64(r.Dy()/2), min(s.targetY-s.scrollY, float64(r.Dy()/2))))
	// Prefetch before either cache edge reaches the viewport. A wheel event
	// does not invalidate in-flight decoding, so continuous input cannot starve it.
	margin := min(r.Dy()/2, max(0, (b.height-r.Dy())/4))
	nearTop := b.top > 0 && position-b.top < margin
	nearBottom := b.top+b.height < b.contentHeight && b.top+b.height-position-r.Dy() < margin
	outside := position < b.top || position+r.Dy() > b.top+b.height
	if !nearTop && !nearBottom && !outside {
		return
	}
	if clampInt(position-max(0, (b.height-r.Dy())/2), 0, max(0, b.contentHeight-b.height)) == b.top {
		return
	}
	t.enqueueRegionJob(regionJob{generation: t.displayRegions.generation, revision: t.displayRegions.documentRevision, request: *s.request,
		width: s.renderWidth(r.Dx()), height: r.Dy(), position: position, background: s.background(t.displayRegions.background)})
}

func (s *regionSurface) covers(height int) bool {
	b := s.bitmap
	return len(b.pixels) > 0 && b.width == s.viewWidth && int(s.scrollY) >= b.top &&
		int(math.Ceil(s.scrollY))+min(height, b.contentHeight-int(s.scrollY)) <= b.top+b.height
}

// paintSecondary touches only the secondary rectangle. Pixels are owned by
// this surface, independently of the terminal's recyclable image atlas.
func (t *Terminal) paintSecondary() image.Rectangle {
	// Headless compositor. Live X previews own a separate drawable and frame.
	if t.conn != nil {
		return image.Rectangle{}
	}
	t.displayRegions.mu.Lock()
	frame, ok := t.regionFrame()
	t.displayRegions.mu.Unlock()
	if !ok {
		return image.Rectangle{}
	}
	renderRegionPixels(frame.surface, frame.viewport, frame.pane, frame.background, framebuf, fbW)
	return frame.pane
}

func renderRegionPixels(surface regionSurface, r, pane image.Rectangle, fallback uint32, pixels []uint32, stride int) {
	bitmap := surface.bitmap
	bg := surface.background(fallback)
	if bitmap.contentHeight > 0 && bitmap.background != 0 {
		// Keep the displayed revision's paper and canvas colors together until
		// its asynchronous replacement succeeds.
		bg = bitmap.background
	}
	// Divider occupies preview pixels, never the Vim grid or its erase area.
	for y := pane.Min.Y; y < pane.Max.Y; y++ {
		for x := pane.Min.X; x < r.Min.X; x++ {
			pixels[y*stride+x] = 0xff777777
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if bitmap.contentHeight > 0 && len(bitmap.pixels) > 0 {
			width := surface.renderWidth(r.Dx())
			sy := int(math.Round((surface.scrollY+float64(y-r.Min.Y))*float64(bitmap.width)/float64(width))) - bitmap.top
			if sy >= 0 && sy < bitmap.height && bitmap.width == width && width >= r.Dx() {
				start := sy*bitmap.width + surface.pan
				copy(pixels[y*stride+r.Min.X:y*stride+r.Max.X], bitmap.pixels[start:start+r.Dx()])
				continue
			}
			for x := r.Min.X; x < r.Max.X; x++ {
				pixel := bg
				logicalX := x - r.Min.X + surface.pan - max(0, (r.Dx()-width)/2)
				if sy >= 0 && sy < bitmap.height && logicalX >= 0 && logicalX < width {
					sx := logicalX * bitmap.width / width
					pixel = bitmap.pixels[sy*bitmap.width+sx]
				}
				pixels[y*stride+x] = pixel
			}
			continue
		}
		if bitmap.width == r.Dx() && bitmap.height == r.Dy() && len(bitmap.pixels) > 0 {
			src := (y - r.Min.Y) * bitmap.width
			copy(pixels[y*stride+r.Min.X:y*stride+r.Max.X], bitmap.pixels[src:src+bitmap.width])
			continue
		}
		for x := r.Min.X; x < r.Max.X; x++ {
			pixel := bg
			if len(bitmap.pixels) > 0 {
				// On resize, show the previous bitmap immediately until the
				// worker finishes rendering at the new size.
				sx := (x - r.Min.X) * bitmap.width / r.Dx()
				sy := (y - r.Min.Y) * bitmap.height / r.Dy()
				pixel = bitmap.pixels[sy*bitmap.width+sx]
			}
			pixels[y*stride+x] = pixel
		}
	}
}

func renderRegionImage(job regionJob) (regionBitmap, error) {
	size, ok := limits.RGBA("preview region", job.width, job.height)
	if !ok {
		return regionBitmap{}, fmt.Errorf("preview dimensions exceed resource limit")
	}
	data, err := limits.ReadFile(job.request.Path)
	if err != nil {
		return regionBitmap{}, err
	}
	result := regionBitmap{pixels: make([]uint32, size/4), width: job.width, height: job.height}
	if isPDF(data) {
		pages := pdfPageCount(data)
		if pages < 1 {
			return regionBitmap{}, fmt.Errorf("cannot read PDF pages")
		}
		page := ((job.request.Options.Page % pages) + pages) % pages
		result.page = page
		bgra, _, ok := renderPDFPageViewport(data, page, job.width, job.height, job.request.Options.FitWidth)
		if !ok {
			return regionBitmap{}, fmt.Errorf("cannot render PDF page")
		}
		for i := range result.pixels {
			p := bgra[i*4:]
			result.pixels[i] = 0xff000000 | uint32(p[2])<<16 | uint32(p[1])<<8 | uint32(p[0])
		}
		return result, nil
	}
	w, h, rgba, failed := decodeImage(data)
	if failed {
		return regionBitmap{}, fmt.Errorf("cannot decode image (expected a static image or PDF)")
	}
	width, height := w, h
	opts := job.request.Options
	if opts.FitContain {
		if int64(w)*int64(job.height) > int64(h)*int64(job.width) {
			width, height = job.width, max(1, int(int64(h)*int64(job.width)/int64(w)))
		} else {
			height, width = job.height, max(1, int(int64(w)*int64(job.height)/int64(h)))
		}
	} else if opts.FitWidth {
		width, height = job.width, max(1, int(int64(h)*int64(job.width)/int64(w)))
	} else if opts.FitHeight {
		height, width = job.height, max(1, int(int64(w)*int64(job.height)/int64(h)))
	}
	for y := 0; y < job.height; y++ {
		for x := 0; x < job.width; x++ {
			pixel := job.background
			if x < width && y < height {
				sx, sy := int(int64(x)*int64(w)/int64(width)), int(int64(y)*int64(h)/int64(height))
				p := rgba[(sy*w+sx)*4:]
				a := uint32(p[3])
				r := (uint32(p[0])*a + ((job.background>>16)&255)*(255-a)) / 255
				g := (uint32(p[1])*a + ((job.background>>8)&255)*(255-a)) / 255
				b := (uint32(p[2])*a + (job.background&255)*(255-a)) / 255
				pixel = 0xff000000 | r<<16 | g<<8 | b
			}
			result.pixels[y*job.width+x] = pixel
		}
	}
	return result, nil
}
