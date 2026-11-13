package frontend

import (
	"fmt"
	"image"
	"math"
	"sort"
	"sync"
	"time"
	"unicode"

	"st-go/limits"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

// The public interface is rectangle-based; this backend currently supports
// adjoining main-left/secondary-right or main-top/secondary-bottom regions,
// plus an application-reserved overlay on the full-size main grid.
// Layout belongs to Terminal.mu. Preview content, navigation and jobs belong
// to mu. Lock order is Terminal.mu -> mu; preview workers never take Terminal.mu.
// Geometry is copied at layout changes so workers never read the terminal grid.
type displayRegionState struct {
	documentCancel        func()
	documentQuery         uint64
	mu                    sync.Mutex
	mainWidth, totalWidth int           // requested ratio, preserved across WM resizes
	mainRows, windowRows  int           // nonzero mainRows selects a stacked layout
	overlay               term.CellRect // positive width keeps the main grid full-sized
	secondary             *regionSurface
	listPreview           *regionSurface // suspended image/PDF while a list is shown
	listVisible           *regionListSurface
	listVisibleY          float64 // last committed frame, not the animation target
	listVisibleEpoch      uint64
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
	metadataJobs          chan regionMetadataJob
	metadataReplies       chan regionMetadataReply
	presenter             regionPresenter
}

type regionReply struct {
	generation uint64
	text       string
}

type regionSurface struct {
	mediaStatus           api.MediaStatus
	media                 api.MediaView
	html                  api.HTMLView
	list                  *regionListSurface
	listSelected          int
	request               *term.RegionImage
	bitmap                regionBitmap
	dirty                 bool
	reportScroll          bool
	reportScrollEdges     bool
	loading, scrollFailed bool
	scrollY, targetY      float64
	lastScroll            time.Time
	viewWidth             int
	seekPosition          *int
	seekPage              *int
	seekPageY             int
	zoom, pan             int // zoom percent of fit-width; zero means 100
	imageMode             imageScaleMode
	sourceWidth           int
	sourceHeight          int
	dragX, dragY          int
	dragging              bool
	viewerPrevious        *regionSurface // committed frame retained while a replacement decodes
	search                *pdfMatches    // immutable painter snapshot
	pendingSearch         *regionSearch
	searchAnchored        bool // selected occurrence remains current at short-page boundaries
}

type regionBitmap struct {
	pixels             []uint32
	width, height      int
	sourceWidth        int
	sourceHeight       int
	page               int // resolved zero-based page, including modulo wrapping
	top, contentHeight int // cached strip origin and full continuous height
	background         uint32
	pageTops           []int // one offset per page plus the document end
}

type imageScaleMode uint8

const (
	imageFitWidth imageScaleMode = iota
	imageFitHeight
	imageOriginal
	imageZoom
)

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
	y               int // page-relative millionths for a new search's anchor
}

var _ term.RegionHooks = (*Terminal)(nil)
var _ term.RegionScrollEdgeHooks = (*Terminal)(nil)

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
		if t.displayRegions.overlay.Width > 0 {
			bounds = t.displayRegions.overlay
			bounds.X, bounds.Y = min(bounds.X, t.cols), min(bounds.Y, t.rows)
			bounds.Width, bounds.Height = min(bounds.Width, t.cols-bounds.X), min(bounds.Height, t.rows-bounds.Y)
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
	s.overlay = term.CellRect{}
	if mainWidth == cols {
		if registry, ok := t.host.(api.MediaRegistry); ok {
			for _, surface := range []*regionSurface{s.secondary, s.listPreview} {
				if surface != nil && surface.media.ID != 0 {
					registry.ReleaseMedia(surface.media.ID)
				}
			}
		}
		s.generation++ // invalidate both pending and in-flight decode results
		s.documentRevision++
		s.secondary = nil
		s.listPreview = nil
		s.listVisible = nil
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

// SetRegionOverlay reuses the retained preview's resource/presentation lifecycle
// while leaving cursor coordinates, the terminal grid and PTY dimensions alone.
func (t *Terminal) SetRegionOverlay(bounds term.CellRect) error {
	cols, rows := t.physicalCols(), t.physicalRows()
	if t.termCore == nil || t.cw < 1 || t.ch < 1 || bounds.X < 0 || bounds.X >= cols || bounds.Y < 0 || bounds.Y >= rows || bounds.Width < 1 || bounds.Width > cols-bounds.X || bounds.Height < 1 || bounds.Height > rows-bounds.Y {
		return fmt.Errorf("overlay must be a positive in-bounds cell rectangle")
	}
	t.pauseRegionPaint(t.termCore.IsPaintPaused())
	s := &t.displayRegions
	s.mu.Lock()
	s.overlay = bounds
	s.mainWidth, s.totalWidth, s.mainRows = 0, 0, 0
	if s.secondary == nil {
		s.secondary = &regionSurface{dirty: true}
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
	if s.secondary != nil && s.overlay.Width == 0 {
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
	_ = t.configureVideo()
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
	if request.Continuous && request.Viewer {
		return fmt.Errorf("a region cannot be both a continuous PDF and an image viewer")
	}
	if request.Background != 0 && (!request.Continuous || request.Background>>24 != 255) {
		return fmt.Errorf("region background requires an opaque color and a continuous PDF view")
	}
	if surface.list != nil {
		if t.displayRegions.listPreview != nil {
			surface = t.displayRegions.listPreview
		} else {
			surface = &regionSurface{}
		}
		t.displayRegions.secondary, t.displayRegions.listPreview = surface, nil
		t.displayRegions.listVisible = nil
	}
	if request.KeepPage && surface.request != nil {
		request.Options.Page = surface.request.Options.Page
		if surface.media.ID != 0 {
			request.Options.Page = max(0, surface.media.Page-1)
		}
	}
	if t.scene() != nil {
		registry := t.host.(api.MediaRegistry)
		resource := registry.RegisterMedia(api.MediaSource{Path: request.Path})
		if resource == 0 {
			return fmt.Errorf("media resource budget exhausted")
		}
		if surface.media.ID != 0 {
			registry.ReleaseMedia(surface.media.ID)
		}
		t.displayRegions.documentRevision++
		if t.displayRegions.documentCancel != nil {
			t.displayRegions.documentCancel()
			t.displayRegions.documentCancel = nil
		}
		surface.search, surface.pendingSearch = nil, nil
		surface.mediaStatus = api.MediaStatus{}
		fit := "contain"
		if request.Continuous {
			fit = "width"
		}
		if request.Viewer {
			fit = "native"
			if request.Options.FitContain {
				fit = "width"
			}
		}
		if request.Options.FitWidth {
			fit = "width"
		}
		if request.Options.FitHeight {
			fit = "height"
		}
		surface.request = &request
		surface.html = api.HTMLView{}
		surface.media = api.MediaView{ID: resource, Generation: t.displayRegions.documentRevision, Page: request.Options.Page + 1, Fit: fit, Continuous: request.Continuous, Background: request.Background, Zoom: request.ViewerZoom}
		surface.loading = true
		view := surface.media
		t.host.(api.MediaObserver).WatchMedia(resource, func(status api.MediaStatus) {
			s := &t.displayRegions
			s.mu.Lock()
			defer s.mu.Unlock()
			if t.closed() || surface.media.ID != status.ID || view.Generation != status.Generation || surface.media.PositionID != status.PositionID {
				return
			}
			surface.mediaStatus = status
			surface.loading = false
			surface.scrollFailed = status.Error != ""
			surface.media.Page = max(1, status.Page)
			surface.media.Position = status.Position
			if surface.reportScroll {
				t.queueRegionReply(regionReply{s.generation, t.regionStatusReply(id, surface)})
			}
		})
		surface.dirty = true
		t.requestRegionPaint()
		return nil
	}
	initialPage := request.Continuous && request.Options.Page > 0 && !request.KeepPage
	request.KeepPage = false
	if request.Viewer {
		if surface.viewerPrevious == nil && len(surface.bitmap.pixels) > 0 {
			previous := *surface
			previous.viewerPrevious = nil
			surface.viewerPrevious = &previous
		}
		surface.scrollY, surface.targetY = 0, 0
		surface.seekPosition, surface.seekPage = nil, nil
		surface.seekPageY = 0
		surface.pan, surface.sourceWidth, surface.sourceHeight = 0, 0, 0
		surface.dragging = false
		surface.zoom = 100
		switch {
		case request.ViewerZoom > 0:
			surface.imageMode = imageZoom
			surface.zoom = request.ViewerZoom
		case request.Options.FitHeight:
			surface.imageMode = imageFitHeight
		case request.Options.FitWidth || request.Options.FitContain:
			surface.imageMode = imageFitWidth
		default:
			surface.imageMode = imageOriginal
		}
	} else if !request.Continuous || surface.request == nil || !surface.request.Continuous {
		surface.viewerPrevious = nil
		surface.scrollY, surface.targetY = 0, 0
		surface.seekPosition = nil
		surface.seekPage = nil
		surface.seekPageY = 0
	}
	surface.request = &request
	surface.html = api.HTMLView{}
	surface.search, surface.pendingSearch = nil, nil
	surface.searchAnchored = false
	surface.dirty = true
	t.displayRegions.documentRevision++
	if initialPage {
		page := request.Options.Page + 1
		surface.seekPage = &page
		surface.seekPageY = 0
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
	return t.clearRegion(id)
}

func (t *Terminal) clearRegion(id term.RegionID) error {
	if t.termCore != nil {
		t.displayRegions.paused = t.termCore.IsPaintPaused()
	}
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if surface.media.ID != 0 {
		t.host.(api.MediaRegistry).ReleaseMedia(surface.media.ID)
		surface.media = api.MediaView{}
	}
	if old := t.displayRegions.listPreview; old != nil && old.media.ID != 0 {
		t.host.(api.MediaRegistry).ReleaseMedia(old.media.ID)
	}
	if t.displayRegions.documentCancel != nil {
		t.displayRegions.documentCancel()
		t.displayRegions.documentCancel = nil
	}
	t.displayRegions.generation++
	t.displayRegions.documentRevision++
	t.cancelRegionDecode()
	surface.request = nil
	surface.html = api.HTMLView{}
	surface.list = nil
	t.displayRegions.listPreview = nil
	t.displayRegions.listVisible = nil
	surface.search, surface.pendingSearch = nil, nil
	surface.searchAnchored = false
	surface.bitmap = regionBitmap{}
	surface.scrollY, surface.targetY = 0, 0
	surface.pan, surface.sourceWidth, surface.sourceHeight = 0, 0, 0
	surface.dragging = false
	surface.viewerPrevious = nil
	surface.seekPosition = nil
	surface.seekPage = nil
	surface.seekPageY = 0
	surface.loading, surface.scrollFailed = false, false
	surface.dirty = true
	t.requestRegionPaint()
	return nil
}

func (t *Terminal) queueRegionImage() {
	s := &t.displayRegions
	s.generation++
	surface := s.secondary
	if surface != nil && surface.media.ID != 0 {
		surface.dirty = true
		t.requestRegionPaint()
		return
	}
	if surface != nil && surface.list != nil {
		list := surface.list
		if list.cw != t.cw || list.ch != t.ch || list.baseline != t.baseline {
			if next, err := newRegionList(list.content, t.cw, t.ch, t.baseline, list); err == nil {
				surface.list = next
			} else {
				limits.Report("region list", "%v", err)
			}
		}
		maximum := surface.list.maximum(s.viewport.Dy())
		surface.scrollY = max(0, min(surface.scrollY, maximum))
		surface.targetY = max(0, min(surface.targetY, maximum))
		surface.dirty = true
		t.cancelRegionDecode()
		t.requestRegionPaint()
		return
	}
	if surface == nil || surface.request == nil {
		t.cancelRegionDecode()
		return
	}
	r := s.viewport
	if r.Empty() {
		return
	}
	width := surface.renderWidth(r.Dx(), r.Dy())
	if surface.bitmap.contentHeight > 0 && surface.viewWidth > 0 && surface.viewWidth != width {
		// Keep the same document coordinate when the fit-width scale changes.
		scale := float64(width) / float64(surface.viewWidth)
		if surface.request.Viewer {
			surface.scrollY = (surface.scrollY+float64(r.Dy())/2)*scale - float64(r.Dy())/2
			surface.targetY = surface.scrollY
			surface.pan = int(math.Round((float64(surface.pan)+float64(r.Dx())/2)*scale - float64(r.Dx())/2))
		} else {
			surface.scrollY *= scale
			surface.targetY *= scale
		}
	}
	surface.viewWidth = width
	if surface.request.Viewer {
		surface.zoom = max(1, int(math.Round(float64(width)*100/float64(max(1, r.Dx())))))
		if surface.imageMode == imageZoom {
			surface.request.ViewerZoom = surface.zoom
		}
	}
	surface.pan = clampInt(surface.pan, 0, max(0, width-r.Dx()))
	surface.scrollY = max(0, min(surface.scrollY, t.regionScrollMaximum()))
	surface.targetY = max(0, min(surface.targetY, t.regionScrollMaximum()))
	surface.scrollFailed = false
	t.enqueueRegionJob(regionJob{generation: s.generation, revision: s.documentRevision, request: *surface.request,
		width: width, height: r.Dy(), background: surface.background(s.background), position: int(surface.scrollY), search: surface.pendingSearch})
}

func (s *regionSurface) zoomPercent() int {
	if s.zoom == 0 {
		return 100
	}
	return s.zoom
}

func (s *regionSurface) renderWidth(width, height int) int {
	if s.request == nil {
		return width
	}
	if s.request.Viewer {
		result := int64(width)
		switch s.imageMode {
		case imageFitHeight:
			if s.sourceWidth > 0 && s.sourceHeight > 0 {
				result = max(1, int64(height)*int64(s.sourceWidth)/int64(s.sourceHeight))
			}
		case imageOriginal:
			if s.sourceWidth > 0 {
				result = int64(s.sourceWidth)
			}
		case imageZoom:
			result = max(1, int64(width)*int64(s.zoomPercent())/100)
		}
		return clampImageViewerWidth(result, height, s.sourceWidth, s.sourceHeight)
	}
	if !s.request.Continuous {
		return width
	}
	return max(1, width*s.zoomPercent()/100)
}

func (s *regionSurface) scrollable() bool {
	return s.request != nil && (s.request.Continuous || s.request.Viewer)
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
		s.metadataJobs = make(chan regionMetadataJob, 1)
		go t.regionDecoder(s.jobs, s.metadataJobs, t.done)
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

func (t *Terminal) regionDecoder(jobs <-chan regionJob, metadata <-chan regionMetadataJob, done <-chan struct{}) {
	var document *scrollPDF
	var viewerImage *scrollImage
	var documentRevision uint64
	var imageRevision uint64
	var metadataCache regionMetadataCache
	defer func() { document.close() }()
	for {
		select {
		case <-done:
			return
		case query := <-metadata:
			t.processRegionMetadata(query, &document, &documentRevision, &metadataCache)
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
				metadataCache = regionMetadataCache{}
				continue
			}
			var bitmap regionBitmap
			var err error
			found := 0
			var matches *pdfMatches
			visibleFirst := false
			if job.request.Viewer {
				document.close()
				document = nil
				if viewerImage == nil || imageRevision != job.revision {
					var data []byte
					data, err = limits.ReadFile(job.request.Path)
					if err == nil {
						viewerImage, err = openScrollImage(data)
						if err == nil {
							imageRevision = job.revision
						}
					}
				}
				if err == nil {
					bitmap, err = viewerImage.renderJob(job)
				}
			} else if job.request.Continuous {
				viewerImage = nil
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
						var tops []int
						matches, tops, err = document.matches(job.width, job.search.text, func() bool {
							t.displayRegions.mu.Lock()
							defer t.displayRegions.mu.Unlock()
							return !t.closed() && job.generation == t.displayRegions.generation
						})
						if matches != nil {
							matches.choose(job.search.page, job.search.y, job.search.direction)
							found, job.position = matches.destination(tops, job.height)
						}
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
				viewerImage = nil
				bitmap, err = renderRegionImage(job)
			}
			t.displayRegions.mu.Lock()
			if job.search != nil && !t.closed() && job.generation == t.displayRegions.generation && t.displayRegions.secondary != nil {
				s := t.displayRegions.secondary
				s.loading = false
				s.search, s.pendingSearch, s.dirty = matches, nil, true
				if err == nil && found > 0 {
					s.searchAnchored = true
					s.scrollY, s.targetY = float64(job.position), float64(job.position)
					x := int(int64(matches.hits[matches.active].box.Min.X) * int64(job.width) / 1e6)
					s.pan = clampInt(x-t.displayRegions.viewport.Dx()/4, 0, max(0, job.width-t.displayRegions.viewport.Dx()))
				}
				t.publishSearchReply(matches)
				t.requestRegionPaint()
			} else if job.search != nil {
				t.displayRegions.mu.Unlock()
				continue
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
	if surface.request != nil && surface.request.Viewer {
		surface.viewerPrevious = nil
		surface.sourceWidth, surface.sourceHeight = bitmap.sourceWidth, bitmap.sourceHeight
		surface.viewWidth = bitmap.width
		r := t.displayRegions.viewport
		surface.zoom = max(1, int(math.Round(float64(bitmap.width)*100/float64(max(1, r.Dx())))))
		if surface.imageMode == imageZoom {
			surface.request.ViewerZoom = surface.zoom
		}
		surface.pan = clampInt(surface.pan, 0, max(0, bitmap.width-r.Dx()))
	}
	if bitmap.contentHeight > 0 {
		maximum := float64(max(0, bitmap.contentHeight-t.displayRegions.viewport.Dy()))
		if surface.seekPosition != nil {
			surface.scrollY = maximum * float64(*surface.seekPosition) / 1e6
			surface.targetY = surface.scrollY
			surface.seekPosition = nil
		}
		if surface.seekPage != nil && len(bitmap.pageTops) > 1 {
			page := clampInt(*surface.seekPage, 1, len(bitmap.pageTops)-1)
			surface.scrollY = float64(bitmap.pageTops[page-1]) + float64(bitmap.pageTops[page]-bitmap.pageTops[page-1])*float64(surface.seekPageY)/1e6
			surface.targetY = surface.scrollY
			surface.seekPage = nil
			surface.seekPageY = 0
		}
		surface.scrollY = max(0, min(surface.scrollY, maximum))
		surface.targetY = max(0, min(surface.targetY, maximum))
		t.queueRegionScroll()
	}
	if request := t.displayRegions.secondary.request; request != nil && !request.Continuous && !request.Viewer {
		request.Options.Page = bitmap.page
	}
	t.displayRegions.secondary.dirty = true
	t.requestRegionPaint()
	if surface.request != nil && surface.request.Viewer {
		r := t.displayRegions.viewport
		if wanted := surface.renderWidth(r.Dx(), r.Dy()); wanted != bitmap.width {
			t.queueRegionImage()
		}
	}
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
	r := image.Rect(0, 0, t.fbW, t.fbH)
	if t.displayRegions.secondary != nil && t.displayRegions.overlay.Width == 0 {
		r = r.Intersect(image.Rect(t.borderpx, t.borderpx, t.borderpx+t.cols*t.cw, t.borderpx+t.rows*t.ch))
	}
	return r
}

func (t *Terminal) secondaryPixels() image.Rectangle {
	r := t.secondaryPanePixels()
	if t.displayRegions.mainRows == 0 && t.displayRegions.overlay.Width == 0 {
		r.Min.X += min(3, max(0, r.Dx()-1))
	}
	return r
}

func (t *Terminal) secondaryPanePixels() image.Rectangle {
	if b := t.displayRegions.overlay; b.Width > 0 {
		// Clip in cells before multiplying, including after a physical shrink.
		x, y := min(b.X, t.cols), min(b.Y, t.rows)
		w, h := min(b.Width, t.cols-x), min(b.Height, t.rows-y)
		return image.Rect(t.borderpx+x*t.cw, t.borderpx+y*t.ch, t.borderpx+(x+w)*t.cw, t.borderpx+(y+h)*t.ch).Intersect(image.Rect(0, 0, t.fbW, t.fbH))
	}
	if t.displayRegions.mainRows > 0 {
		return image.Rect(t.borderpx, t.borderpx+t.rows*t.ch, t.borderpx+t.cols*t.cw, t.borderpx+t.physicalRows()*t.ch).Intersect(image.Rect(0, 0, t.fbW, t.fbH))
	}
	return image.Rect(t.borderpx+t.cols*t.cw, t.borderpx, t.borderpx+t.physicalCols()*t.cw, t.borderpx+t.rows*t.ch).Intersect(image.Rect(0, 0, t.fbW, t.fbH))
}

func (t *Terminal) overSecondary(px, py int) bool {
	return t.displayRegions.secondary != nil && image.Pt(px, py).In(t.secondaryPanePixels())
}

func (t *Terminal) regionMousePress(button byte, state uint16, px, py int) bool {
	if t.buttons != 0 || !t.overSecondary(px, py) {
		return false // a main-region drag keeps ownership through its release
	}
	t.regionButtons |= buttonmask(button)
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	surface := t.displayRegions.secondary
	if surface.list != nil {
		t.listMousePress(button, px, py)
		return true
	}
	if button == 4 || button == 5 {
		delta := 1
		if button == 4 {
			delta = -1
		}
		if surface.request != nil && surface.request.Viewer && state&ControlMask != 0 {
			t.zoomRegionImage(surface, -delta)
			if t.termCore != nil {
				t.termCore.WriteToTTY([]byte(t.regionStatusReply(term.SecondaryRegion, surface)), false)
			}
			return true
		}
		if surface.scrollable() {
			if t.reportRegionScrollEdge(term.SecondaryRegion, surface, delta) {
				return true
			}
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
	if button == 1 && surface.request != nil && surface.request.Viewer {
		surface.dragging = true
		surface.dragX, surface.dragY = px, py
	}
	return true
}

func (t *Terminal) regionMouseMotion(px, py int) {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	s := t.displayRegions.secondary
	if s == nil || !s.dragging || s.request == nil || !s.request.Viewer {
		return
	}
	r := t.displayRegions.viewport
	s.pan = clampInt(s.pan+s.dragX-px, 0, max(0, s.renderWidth(r.Dx(), r.Dy())-r.Dx()))
	s.scrollY = max(0, min(s.scrollY+float64(s.dragY-py), t.regionScrollMaximum()))
	s.targetY = s.scrollY
	s.dragX, s.dragY = px, py
	s.dirty = true
	t.queueRegionScroll()
	t.requestRegionPaint()
}

func (t *Terminal) regionMouseRelease(button byte) {
	if button != 1 {
		return
	}
	t.displayRegions.mu.Lock()
	if s := t.displayRegions.secondary; s != nil {
		s.dragging = false
	}
	t.displayRegions.mu.Unlock()
}

func (t *Terminal) SetRegionScrollReporting(id term.RegionID, enabled bool) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	surface, err := t.secondaryRegion(id)
	if err == nil {
		surface.reportScroll = enabled
		if enabled {
			surface.reportScrollEdges = false
		}
	}
	return err
}

func (t *Terminal) SetRegionScrollEdgeReporting(id term.RegionID, enabled bool) error {
	t.displayRegions.mu.Lock()
	defer t.displayRegions.mu.Unlock()
	surface, err := t.secondaryRegion(id)
	if err == nil {
		surface.reportScrollEdges = enabled
		if enabled {
			surface.reportScroll = false
		}
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
	if s.request == nil || !s.request.Continuous {
		return fmt.Errorf("search requires a loaded continuous PDF")
	}
	if s.media.ID != 0 {
		return t.findSceneDocument(s, text, direction, next)
	}
	if text == "" {
		s.search, s.pendingSearch, s.dirty = nil, nil, true
		s.searchAnchored = false
		t.queueRegionImage()
		t.requestRegionPaint()
		return nil
	}
	if len(s.bitmap.pageTops) < 2 || len(text) > limits.PreviewSearchBytes || (direction != 1 && direction != -1) {
		return fmt.Errorf("invalid PDF search")
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf("PDF search contains control characters")
		}
	}
	page := s.currentPage() - 1
	if next && s.pendingSearch == nil && s.search != nil && s.search.query == text {
		matches := *s.search
		if len(matches.hits) > 0 {
			matches.active = (matches.active + direction + len(matches.hits)) % len(matches.hits)
			s.search = &matches
			t.revealSearchMatch()
		}
		t.publishSearchReply(s.search)
		return nil
	}
	logicalY := int(s.scrollY * float64(s.bitmap.width) / float64(max(1, s.viewWidth)))
	y := clampInt(int(int64(logicalY-s.bitmap.pageTops[page])*1e6/int64(max(1, s.bitmap.pageTops[page+1]-s.bitmap.pageTops[page]))), 0, 1000000)
	s.seekPage, s.seekPosition = nil, nil
	s.search = nil
	s.searchAnchored = false
	s.pendingSearch = &regionSearch{text: text, page: page, direction: direction, y: y}
	s.dirty = true
	t.displayRegions.generation++
	r := t.secondaryPixels()
	t.enqueueRegionJob(regionJob{generation: t.displayRegions.generation, revision: t.displayRegions.documentRevision,
		request: *s.request, width: s.renderWidth(r.Dx(), r.Dy()), height: r.Dy(), background: s.background(t.colorAt(ColDefaultBg)),
		search: s.pendingSearch})
	t.requestRegionPaint()
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
	if surface.media.ID != 0 {
		switch event.Kind {
		case "info":
			t.termCore.WriteToTTY([]byte(t.regionStatusReply(id, surface)), false)
			return nil
		case "scroll":
			surface.media.Scroll += event.Value
		case "view":
			surface.media.Scroll += event.Value * t.displayRegions.viewport.Dy() / 1000000 / max(1, t.ch)
		case "position":
			surface.media.PositionID++
			surface.media.Position = clampInt(event.Value, 0, 1000000)
			surface.media.Scroll = 0
		case "seek":
			surface.media.Page = max(1, event.Value)
			surface.media.Position = 0
			surface.media.PositionID++
		case "page":
			surface.media.Page = max(1, surface.media.Page+event.Value)
			surface.media.Position = 0
			surface.media.PositionID++
		case "zoom":
			surface.media.Zoom = clampInt(event.Value, 1, term.RegionViewerZoomMaximum)
		case "zoomstep":
			surface.media.Zoom = clampInt(int(float64(max(100, surface.media.Zoom))*math.Pow(1.25, float64(clampInt(event.Value, -20, 20)))), 1, term.RegionViewerZoomMaximum)
		case "fit-width":
			surface.media.Fit = "width"
		case "fit-height":
			surface.media.Fit = "height"
		case "fit-original":
			surface.media.Fit = "native"
		default:
			return fmt.Errorf("unsupported media navigation")
		}
		surface.dirty = true
		t.requestRegionPaint()
		return nil
	}
	if surface.html.Document != nil {
		switch event.Kind {
		case "position":
			if event.Value < 0 || event.Value > 1000000 {
				return fmt.Errorf("invalid HTML position")
			}
			surface.html.PositionID++
			surface.html.Position = event.Value
			surface.html.Scroll = 0
		case "scroll":
			surface.html.Scroll += event.Value
		case "view":
			surface.html.Scroll += event.Value * t.displayRegions.viewport.Dy() / 1000000 / max(1, t.ch)
		default:
			return fmt.Errorf("unsupported HTML navigation")
		}
		surface.dirty = true
		t.requestRegionPaint()
		return nil
	}
	if surface.list != nil {
		switch event.Kind {
		case "scroll":
			t.scrollRegionBy(event.Value)
		case "view":
			t.scrollRegionPixels(float64(event.Value) * float64(t.displayRegions.viewport.Dy()) / 1e6)
		case "position":
			if event.Value < 0 || event.Value > 1000000 {
				return fmt.Errorf("invalid list position")
			}
			surface.scrollY = t.regionScrollMaximum() * float64(event.Value) / 1e6
			surface.targetY = surface.scrollY
			surface.dirty = true
			t.requestRegionPaint()
		default:
			return fmt.Errorf("unsupported list navigation")
		}
		return nil
	}
	if event.Kind == "info" {
		t.termCore.WriteToTTY([]byte(t.regionStatusReply(id, surface)), false)
		return nil
	}
	switch event.Kind {
	case "fit-width", "fit-height", "fit-original":
		if surface.request == nil || !surface.request.Viewer {
			return fmt.Errorf("fit navigation requires an image viewer")
		}
		switch event.Kind {
		case "fit-height":
			surface.imageMode = imageFitHeight
			surface.request.Options.FitContain, surface.request.Options.FitWidth, surface.request.Options.FitHeight = false, false, true
		case "fit-original":
			surface.imageMode = imageOriginal
			surface.request.Options.FitContain, surface.request.Options.FitWidth, surface.request.Options.FitHeight = false, false, false
		default:
			surface.imageMode = imageFitWidth
			surface.request.Options.FitContain, surface.request.Options.FitWidth, surface.request.Options.FitHeight = false, true, false
		}
		surface.request.ViewerZoom = 0
		surface.pan, surface.scrollY, surface.targetY = 0, 0, 0
		t.queueRegionImage()
		return nil
	case "zoomstep":
		if surface.request == nil || !surface.request.Viewer || event.Value == 0 {
			return fmt.Errorf("zoomstep requires an image viewer and a nonzero direction")
		}
		t.zoomRegionImage(surface, event.Value)
		return nil
	case "page", "pages", "page-position", "view", "zoom", "pan":
		if !surface.scrollable() {
			return fmt.Errorf("navigation requires a continuous document or image viewer")
		}
		switch event.Kind {
		case "zoom":
			if surface.request.Viewer {
				if event.Value < 1 || event.Value > term.RegionViewerZoomMaximum {
					return fmt.Errorf("image zoom must be 1..%d percent", term.RegionViewerZoomMaximum)
				}
				if event.Value != surface.zoomPercent() || surface.imageMode != imageZoom {
					surface.zoom = event.Value
					surface.imageMode = imageZoom
					surface.request.ViewerZoom = event.Value
					surface.request.Options.FitContain, surface.request.Options.FitWidth, surface.request.Options.FitHeight = false, false, false
					t.queueRegionImage()
				}
				return nil
			}
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
			if !t.reportRegionScrollEdge(id, surface, event.Value) {
				t.scrollRegionPixels(float64(event.Value) * float64(t.secondaryPixels().Dy()) / 1e6)
			}
		case "page", "pages", "page-position":
			if !surface.request.Continuous {
				return fmt.Errorf("page navigation requires a continuous PDF")
			}
			page := event.Value
			fraction := 0
			if event.Kind == "page-position" {
				page = event.Page
				fraction = clampInt(event.Value, 0, 1000000)
			}
			if event.Kind == "pages" {
				page += surface.currentPage()
			}
			surface.searchAnchored = false
			if page < 1 {
				page = 1
			}
			if surface.loading || len(surface.bitmap.pageTops) < 2 {
				surface.seekPage = &page
				surface.seekPageY = fraction
				surface.seekPosition = nil
			} else {
				page = min(page, len(surface.bitmap.pageTops)-1)
				surface.scrollY = min(float64(surface.bitmap.pageTops[page-1])+float64(surface.bitmap.pageTops[page]-surface.bitmap.pageTops[page-1])*float64(fraction)/1e6, t.regionScrollMaximum())
				surface.targetY = surface.scrollY
				surface.dirty = true
				t.queueRegionScroll()
				t.requestRegionPaint()
			}
		}
		return nil
	}
	if event.Kind == "position" {
		surface.searchAnchored = false
		if event.Value < 0 || event.Value > 1000000 || !surface.scrollable() {
			return fmt.Errorf("position requires a scrollable region and a value from 0 to 1000000")
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
	if event.Kind == "scroll" && surface.scrollable() {
		if !t.reportRegionScrollEdge(id, surface, event.Value) {
			t.scrollRegionBy(event.Value)
		}
		return nil
	}
	if (event.Kind != "seek" && event.Kind != "scroll") || !surface.reportScroll {
		return fmt.Errorf("region navigation events are not enabled")
	}
	t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion %s event %s %d\x1b\\", id, event.Kind, event.Value)), false)
	return nil
}

// reportRegionScrollEdge hands only an outward movement at a committed/target
// edge back to applications that render one page per raster. Ordinary motion
// stays local and smooth. targetY is used so a quick wheel burst need not wait
// for easing to finish before it can cross the page boundary.
// The display-region mutex is held by every caller.
func (t *Terminal) reportRegionScrollEdge(id term.RegionID, surface *regionSurface, direction int) bool {
	if !surface.reportScrollEdges || direction == 0 || surface.loading || surface.request == nil || !surface.request.Viewer {
		return false
	}
	maximum := t.regionScrollMaximum()
	atEdge := maximum <= 0 || direction < 0 && surface.targetY <= 0 || direction > 0 && surface.targetY >= maximum
	if !atEdge {
		return false
	}
	if t.termCore == nil {
		return false
	}
	direction = min(1, max(-1, direction))
	t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion %s event scroll %d\x1b\\", id, direction)), false)
	return true
}

// regionStatusReply is also emitted immediately for host-owned Ctrl+wheel
// zoom, so the client can preserve that mode if the next key changes images.
// The display-region mutex is held by every caller.
func (t *Terminal) regionStatusReply(id term.RegionID, surface *regionSurface) string {
	if surface.media.ID != 0 {
		loading, failed := 0, 0
		if surface.loading {
			loading = 1
		}
		if surface.scrollFailed {
			failed = 1
		}
		m := surface.mediaStatus
		reply := fmt.Sprintf("\x1bPregion %s status %d %d %d %d %d %d\x1b\\", id, max(1, m.Page), m.Pages, m.Position, max(100, surface.media.Zoom), loading, failed)
		if surface.request != nil && surface.request.Viewer {
			reply += fmt.Sprintf("\x1bPregion %s image-status %d %d %d %d %s %d %d\x1b\\", id, m.Width, m.Height, m.Position, max(100, surface.media.Zoom), surface.media.Fit, loading, failed)
		}
		return reply
	}
	page, pages := surface.currentPage(), max(0, len(surface.bitmap.pageTops)-1)
	if surface.request != nil && surface.request.Viewer && surface.sourceWidth > 0 {
		page, pages = 1, 1
	}
	position := int(surface.scrollY / max(1, t.regionScrollMaximum()) * 1e6)
	loading, failed := 0, 0
	if surface.loading && !surface.covers(t.displayRegions.viewport.Dy()) {
		loading = 1
	}
	if surface.scrollFailed {
		failed = 1
	}
	reply := fmt.Sprintf("\x1bPregion %s status %d %d %d %d %d %d\x1b\\", id, page, pages, clampInt(position, 0, 1000000), surface.zoomPercent(), loading, failed)
	if surface.request != nil && surface.request.Viewer {
		reply += fmt.Sprintf("\x1bPregion %s image-status %d %d %d %d %s %d %d\x1b\\", id,
			surface.sourceWidth, surface.sourceHeight, clampInt(position, 0, 1000000), surface.zoomPercent(), surface.imageModeName(), loading, failed)
	}
	return reply
}

func (s *regionSurface) imageModeName() string {
	switch s.imageMode {
	case imageFitHeight:
		return "height"
	case imageOriginal:
		return "original"
	case imageZoom:
		return "zoom"
	default:
		return "width"
	}
}

// zoomRegionImage changes a raster viewer relative to its current fit-width
// percentage. Keeping this in the frontend lets Ctrl+wheel work before the TUI
// receives another status reply and keeps all backends behaviorally identical.
// The display-region mutex is held by every caller.
func (t *Terminal) zoomRegionImage(surface *regionSurface, steps int) {
	steps = clampInt(steps, -20, 20)
	zoom := surface.zoomPercent()
	initial := zoom
	for steps > 0 {
		if zoom >= term.RegionViewerZoomMaximum {
			break
		}
		next := max(zoom+1, (zoom*5+3)/4)
		zoom = min(term.RegionViewerZoomMaximum, next)
		steps--
	}
	for steps < 0 {
		if zoom > term.RegionViewerZoomMaximum {
			zoom = term.RegionViewerZoomMaximum
			steps++
			continue
		}
		next := min(zoom-1, zoom*4/5)
		zoom = max(1, next)
		steps++
	}
	if zoom == initial {
		return
	}
	surface.zoom = zoom
	surface.imageMode = imageZoom
	surface.request.ViewerZoom = zoom
	surface.request.Options.FitContain, surface.request.Options.FitWidth, surface.request.Options.FitHeight = false, false, false
	t.queueRegionImage()
}

func (s *regionSurface) currentPage() int {
	if s.searchAnchored && s.search != nil && s.search.active >= 0 && s.search.active < len(s.search.hits) {
		return s.search.hits[s.search.active].page + 1
	}
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
	s.searchAnchored = false
	maximum := t.regionScrollMaximum()
	s.targetY = max(0, min(s.targetY+pixels, maximum))
	// Give the input event an immediate first frame, then ease at display rate.
	now := time.Now()
	s.lastScroll = now.Add(-16 * time.Millisecond)
	t.tickRegionScroll(now)
}

func (t *Terminal) regionScrollMaximum() float64 {
	if list := t.displayRegions.secondary.list; list != nil {
		return list.maximum(t.displayRegions.viewport.Dy())
	}
	b := t.displayRegions.secondary.bitmap
	r := t.displayRegions.viewport
	return max(0, float64(b.contentHeight)*float64(t.displayRegions.secondary.renderWidth(r.Dx(), r.Dy()))/float64(max(1, b.width))-float64(r.Dy()))
}

func (t *Terminal) tickRegionScroll(now time.Time) {
	s := t.displayRegions.secondary
	if s == nil || s.list == nil && !s.scrollable() || s.scrollY == s.targetY {
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
	if s == nil || !s.scrollable() || s.loading || s.scrollFailed || s.bitmap.contentHeight == 0 {
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
		width: s.renderWidth(r.Dx(), r.Dy()), height: r.Dy(), position: position, background: s.background(t.displayRegions.background)})
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
	if t.host != nil {
		return image.Rectangle{}
	}
	t.displayRegions.mu.Lock()
	frame, ok := t.regionFrame()
	t.displayRegions.mu.Unlock()
	if !ok {
		return image.Rectangle{}
	}
	renderRegionPixels(frame.surface, frame.viewport, frame.pane, frame.background, t.framebuf, t.fbW)
	t.noteRegionListFrame(frame)
	return frame.pane
}

func renderRegionPixels(surface regionSurface, r, pane image.Rectangle, fallback uint32, pixels []uint32, stride int) {
	if surface.list != nil {
		renderRegionList(surface, r, pane, pixels, stride)
		return
	}
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
			width := surface.renderWidth(r.Dx(), r.Dy())
			viewY := float64(y - r.Min.Y)
			if surface.request != nil && surface.request.Viewer && bitmap.contentHeight < r.Dy() {
				viewY -= float64(r.Dy()-bitmap.contentHeight) / 2
			}
			sy := int(math.Round((surface.scrollY+viewY)*float64(bitmap.width)/float64(width))) - bitmap.top
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
	paintSearchMatches(surface, r, pixels, stride)
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
