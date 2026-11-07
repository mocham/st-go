package main

import (
	"fmt"
	"image"
	"math"
	"time"

	"st-go/limits"
	"st-go/term"
)

// The public interface is rectangle-based; this backend currently supports
// only adjoining, full-height main/secondary regions. All state below belongs
// to Terminal.mu. The decoder worker receives immutable snapshots.
type displayRegionState struct {
	mainWidth, totalWidth int // requested ratio, preserved across WM resizes
	secondary             *regionSurface
	generation            uint64
	jobs                  chan regionJob // one pending job, latest request wins
	fullDirty             bool
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
}

type regionBitmap struct {
	pixels             []uint32
	width, height      int
	page               int // resolved zero-based page, including modulo wrapping
	top, contentHeight int // cached strip origin and full continuous height
}

type regionJob struct {
	generation    uint64
	request       term.RegionImage
	width, height int
	background    uint32
	position      int
}

var _ term.RegionHooks = (*Terminal)(nil)

func (t *Terminal) physicalCols() int {
	if t.windowCols > 0 {
		return t.windowCols
	}
	return t.cols
}

func (t *Terminal) Regions() []term.DisplayRegion {
	regions := []term.DisplayRegion{{ID: term.MainRegion, Bounds: term.CellRect{Width: t.cols, Height: t.rows}}}
	if t.displayRegions.secondary != nil {
		regions = append(regions, term.DisplayRegion{ID: term.SecondaryRegion, Bounds: term.CellRect{
			X: t.cols, Width: t.physicalCols() - t.cols, Height: t.rows,
		}})
	}
	return regions
}

func validateDisplayLayout(regions []term.DisplayRegion, cols, rows int) (int, error) {
	if len(regions) == 0 {
		return cols, nil
	}
	if len(regions) > 2 {
		return 0, fmt.Errorf("only main-left/secondary-right layouts are supported")
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
		if b.X < 0 || b.X >= cols || b.Y != 0 || b.Width < 1 || b.Width > cols-b.X || b.Height != rows {
			return 0, fmt.Errorf("regions must be in-bounds, positive, and full-height")
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
	if !seen[term.MainRegion] || main.X != 0 {
		return 0, fmt.Errorf("main must start at the left edge")
	}
	if len(regions) == 1 {
		if main.Width != cols {
			return 0, fmt.Errorf("a single main region must fill the window")
		}
	} else if secondary.X != main.Width || secondary.Width != cols-main.Width {
		return 0, fmt.Errorf("secondary must adjoin main and reach the right edge")
	}
	return main.Width, nil
}

func (t *Terminal) SetRegions(regions []term.DisplayRegion) error {
	if t.termCore == nil || t.cw < 1 || t.ch < 1 {
		return fmt.Errorf("terminal display is not initialized")
	}
	cols := t.physicalCols()
	mainWidth, err := validateDisplayLayout(regions, cols, t.rows)
	if err != nil {
		return err
	}
	s := &t.displayRegions
	if mainWidth == cols {
		s.generation++ // invalidate both pending and in-flight decode results
		s.secondary = nil
		t.cancelRegionDecode()
		s.mainWidth, s.totalWidth = 0, 0
	} else {
		s.mainWidth, s.totalWidth = mainWidth, cols
		if s.secondary == nil {
			s.secondary = &regionSurface{dirty: true}
		}
	}
	t.applyDisplayLayout(cols, t.rows)
	return nil
}

// applyDisplayLayout also runs after a physical resize. At a one-column window
// the preview is temporarily hidden; the requested ratio and content survive.
func (t *Terminal) applyDisplayLayout(cols, rows int) {
	s := &t.displayRegions
	mainCols := cols
	if s.secondary != nil && cols > 1 {
		mainCols = clampInt(int(int64(cols)*int64(s.mainWidth)/int64(s.totalWidth)), 1, cols-1)
	}
	changed := mainCols != t.cols || rows != t.rows
	t.windowCols, t.cols, t.rows = cols, mainCols, rows
	s.fullDirty = true
	if s.secondary != nil {
		s.secondary.dirty = true
		t.queueRegionImage()
	}
	if t.termCore != nil {
		if t.termCore.Cols() != mainCols || t.termCore.Rows() != rows {
			t.termCore.Tresize(mainCols, rows)
		}
		if changed && t.ttyResize != nil {
			t.ttyResize(rows, mainCols)
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
	if request.KeepPage && surface.request != nil {
		request.Options.Page = surface.request.Options.Page
	}
	request.KeepPage = false
	if !request.Continuous || surface.request == nil || !surface.request.Continuous {
		surface.scrollY, surface.targetY = 0, 0
		surface.seekPosition = nil
	}
	surface.request = &request
	t.queueRegionImage()
	return nil
}

func (t *Terminal) ClearRegion(id term.RegionID) error {
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
	surface.loading, surface.scrollFailed = false, false
	surface.dirty = true
	t.requestRegionPaint()
	return nil
}

func (t *Terminal) queueRegionImage() {
	s := &t.displayRegions
	s.generation++
	surface := s.secondary
	if surface == nil || surface.request == nil || t.physicalCols() <= t.cols {
		t.cancelRegionDecode()
		return
	}
	r := t.secondaryPixels()
	if r.Empty() {
		return
	}
	if surface.bitmap.contentHeight > 0 && surface.viewWidth > 0 && surface.viewWidth != r.Dx() {
		// Keep the same document coordinate when the fit-width scale changes.
		scale := float64(r.Dx()) / float64(surface.viewWidth)
		surface.scrollY *= scale
		surface.targetY *= scale
	}
	surface.viewWidth = r.Dx()
	surface.scrollFailed = false
	t.enqueueRegionJob(regionJob{generation: s.generation, request: *surface.request,
		width: r.Dx(), height: r.Dy(), background: t.colorAt(ColDefaultBg), position: int(surface.scrollY)})
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
	// is serialized by Terminal.mu, so the final send always has room.
	select {
	case <-s.jobs:
	default:
	}
	s.jobs <- job
}

func (t *Terminal) regionDecoder(jobs <-chan regionJob, done <-chan struct{}) {
	var document *scrollPDF
	var documentGeneration uint64
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
			if job.request.Path == "" {
				document.close()
				document = nil
				continue
			}
			var bitmap regionBitmap
			var err error
			if job.request.Continuous {
				if document == nil || documentGeneration != job.generation {
					var data []byte
					data, err = limits.ReadFile(job.request.Path)
					if err == nil {
						var next *scrollPDF
						next, err = openScrollPDF(data)
						if err == nil {
							document.close()
							document, documentGeneration = next, job.generation
						}
					}
				}
				if err == nil {
					bitmap, err = document.render(job.width, job.height, job.position)
				}
			} else {
				document.close()
				document = nil
				bitmap, err = renderRegionImage(job)
			}
			t.mu.Lock()
			t.acceptRegionImage(job, bitmap, err)
			t.mu.Unlock()
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
	surface.bitmap = bitmap
	if bitmap.contentHeight > 0 {
		maximum := float64(max(0, bitmap.contentHeight-t.secondaryPixels().Dy()))
		if surface.seekPosition != nil {
			surface.scrollY = maximum * float64(*surface.seekPosition) / 1e6
			surface.targetY = surface.scrollY
			surface.seekPosition = nil
		}
		surface.scrollY = max(0, min(surface.scrollY, maximum))
		surface.targetY = max(0, min(surface.targetY, maximum))
		t.queueRegionScroll()
	}
	if request := t.displayRegions.secondary.request; request != nil {
		request.Options.Page = bitmap.page
	}
	t.displayRegions.secondary.dirty = true
	t.requestRegionPaint()
}

func (t *Terminal) requestRegionPaint() {
	// Queue a tiny main-grid damage marker too, so synchronized-output resume
	// and the existing PTY latency scheduler see pending region work.
	if t.termCore != nil {
		t.termCore.Invalidate(term.Region{})
		t.termCore.PaintDirty()
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
	r.Min.X += min(3, max(0, r.Dx()-1))
	return r
}

func (t *Terminal) secondaryPanePixels() image.Rectangle {
	return image.Rect(t.borderpx+t.cols*t.cw, t.borderpx, t.borderpx+t.physicalCols()*t.cw, t.borderpx+t.rows*t.ch).Intersect(image.Rect(0, 0, fbW, fbH))
}

func (t *Terminal) overSecondary(px int) bool {
	return t.displayRegions.secondary != nil && t.physicalCols() > t.cols && px >= t.borderpx+t.cols*t.cw
}

func (t *Terminal) regionMousePress(button byte, px int) bool {
	if buttons != 0 || !t.overSecondary(px) {
		return false // a main-region drag keeps ownership through its release
	}
	t.regionButtons |= buttonmask(button)
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
			_ = t.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "scroll", Value: delta})
			return true
		}
		if surface.request == nil {
			return true
		}
		req := *surface.request
		req.Options.Page += delta
		_ = t.OpenRegion(term.SecondaryRegion, req)
	}
	return true
}

func (t *Terminal) SetRegionScrollReporting(id term.RegionID, enabled bool) error {
	surface, err := t.secondaryRegion(id)
	if err == nil {
		surface.reportScroll = enabled
	}
	return err
}

func (t *Terminal) NavigateRegion(id term.RegionID, event term.RegionNavigation) error {
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if (event.Kind != "seek" && event.Kind != "scroll" && event.Kind != "position") || event.Value < -limits.MaxBytes || event.Value > limits.MaxBytes || (event.Kind == "seek" && event.Value < 1) {
		return fmt.Errorf("invalid region navigation event")
	}
	if event.Kind == "position" {
		if event.Value < 0 || event.Value > 1000000 || surface.request == nil || !surface.request.Continuous {
			return fmt.Errorf("position requires a continuous PDF and a value from 0 to 1000000")
		}
		if surface.loading {
			value := event.Value
			surface.seekPosition = &value
		}
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
	if !surface.reportScroll {
		return fmt.Errorf("region navigation events are not enabled")
	}
	t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion %s event %s %d\x1b\\", id, event.Kind, event.Value)), false)
	return nil
}

func (t *Terminal) scrollRegionBy(steps int) {
	s := t.displayRegions.secondary
	maximum := t.regionScrollMaximum()
	s.targetY = max(0, min(s.targetY+float64(steps)*float64(max(24, 3*t.ch)), maximum))
	// Give the input event an immediate first frame, then ease at display rate.
	now := time.Now()
	s.lastScroll = now.Add(-16 * time.Millisecond)
	t.tickRegionScroll(now)
}

func (t *Terminal) regionScrollMaximum() float64 {
	b := t.displayRegions.secondary.bitmap
	r := t.secondaryPixels()
	return max(0, float64(b.contentHeight)*float64(r.Dx())/float64(max(1, b.width))-float64(r.Dy()))
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
	r := t.secondaryPixels()
	if r.Empty() {
		return
	}
	b := s.bitmap
	position := int(s.scrollY + max(-float64(r.Dy()/2), min(s.targetY-s.scrollY, float64(r.Dy()/2))))
	// Prefetch before either cache edge reaches the viewport. A wheel event
	// does not invalidate in-flight decoding, so continuous input cannot starve it.
	nearTop := b.top > 0 && position-b.top < r.Dy()/2
	nearBottom := b.top+b.height < b.contentHeight && b.top+b.height-position-r.Dy() < r.Dy()/2
	outside := position < b.top || position+r.Dy() > b.top+b.height
	if !nearTop && !nearBottom && !outside {
		return
	}
	if clampInt(position-max(0, (b.height-r.Dy())/2), 0, max(0, b.contentHeight-b.height)) == b.top {
		return
	}
	t.enqueueRegionJob(regionJob{generation: t.displayRegions.generation, request: *s.request,
		width: r.Dx(), height: r.Dy(), position: position, background: t.colorAt(ColDefaultBg)})
}

// paintSecondary touches only the secondary rectangle. Pixels are owned by
// this surface, independently of the terminal's recyclable image atlas.
func (t *Terminal) paintSecondary() image.Rectangle {
	surface := t.displayRegions.secondary
	if surface == nil || !surface.dirty {
		return image.Rectangle{}
	}
	surface.dirty = false
	r := t.secondaryPixels()
	pane := t.secondaryPanePixels()
	bitmap := surface.bitmap
	bg := t.colorAt(ColDefaultBg)
	// Divider occupies preview pixels, never the Vim grid or its erase area.
	for y := pane.Min.Y; y < pane.Max.Y; y++ {
		for x := pane.Min.X; x < r.Min.X; x++ {
			framebuf[y*fbW+x] = 0xff777777
		}
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if bitmap.contentHeight > 0 && len(bitmap.pixels) > 0 {
			sy := int(math.Round((surface.scrollY+float64(y-r.Min.Y))*float64(bitmap.width)/float64(max(1, r.Dx())))) - bitmap.top
			if sy >= 0 && sy < bitmap.height && bitmap.width == r.Dx() {
				copy(framebuf[y*fbW+r.Min.X:y*fbW+r.Max.X], bitmap.pixels[sy*bitmap.width:(sy+1)*bitmap.width])
				continue
			}
			for x := r.Min.X; x < r.Max.X; x++ {
				pixel := uint32(0xffffffff)
				if sy >= 0 && sy < bitmap.height {
					sx := (x - r.Min.X) * bitmap.width / max(1, r.Dx())
					pixel = bitmap.pixels[sy*bitmap.width+sx]
				}
				framebuf[y*fbW+x] = pixel
			}
			continue
		}
		if bitmap.width == r.Dx() && bitmap.height == r.Dy() && len(bitmap.pixels) > 0 {
			src := (y - r.Min.Y) * bitmap.width
			copy(framebuf[y*fbW+r.Min.X:y*fbW+r.Max.X], bitmap.pixels[src:src+bitmap.width])
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
			framebuf[y*fbW+x] = pixel
		}
	}
	return pane
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
