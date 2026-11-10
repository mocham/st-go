package frontend

import (
	"fmt"

	"st-go/limits"
)

// scrollImage is confined to the retained-region decoder worker. The source
// stays decoded while the viewer pans or zooms, and only a bounded strip of the
// scaled image is published to the presenter.
type scrollImage struct {
	width, height int
	rgba          []byte
	cache         regionBitmap
}

func clampImageViewerWidth(width int64, height, sourceWidth, sourceHeight int) int {
	width = max(int64(1), width)
	rows := int64(max(1, height))
	if sourceWidth > 0 && sourceHeight > 0 {
		total := max(int64(1), int64(sourceHeight)*width/int64(sourceWidth))
		rows = min(rows, total)
	}
	// render must retain at least the visible image rows. Clamp here so even
	// extreme zoom requests stay within the strip-cache budget.
	maximum := max(int64(1), int64(limits.PreviewCacheBytes/4)/rows)
	return int(min(width, maximum))
}

func openScrollImage(data []byte) (*scrollImage, error) {
	w, h, rgba, failed := decodeImage(data)
	if failed || w < 1 || h < 1 {
		return nil, fmt.Errorf("cannot decode viewer image")
	}
	return &scrollImage{width: w, height: h, rgba: rgba}, nil
}

func (i *scrollImage) renderJob(job regionJob) (regionBitmap, error) {
	if i == nil || i.width < 1 || i.height < 1 {
		return regionBitmap{}, fmt.Errorf("invalid image viewer source")
	}
	width := int64(job.width)
	switch {
	case job.request.Options.FitHeight:
		width = max(int64(1), int64(job.height)*int64(i.width)/int64(i.height))
	case job.request.ViewerZoom == 0 && !job.request.Options.FitWidth && !job.request.Options.FitContain:
		width = int64(i.width)
	}
	return i.render(clampImageViewerWidth(width, job.height, i.width, i.height), job.height, job.position, job.background, limits.PreviewScreens)
}

func (i *scrollImage) render(width, height, position int, background uint32, screens int) (regionBitmap, error) {
	if i == nil || i.width < 1 || i.height < 1 || width < 1 || height < 1 {
		return regionBitmap{}, fmt.Errorf("invalid image viewer dimensions")
	}
	total64 := int64(i.height) * int64(width) / int64(i.width)
	if total64 < 1 {
		total64 = 1
	}
	if total64 > limits.PreviewHeight {
		return regionBitmap{}, fmt.Errorf("scaled image exceeds height limit")
	}
	total := int(total64)
	rows := min(total, screens*height, limits.PreviewCacheBytes/4/width)
	if rows < min(total, height) {
		return regionBitmap{}, fmt.Errorf("image viewer viewport exceeds cache budget")
	}
	top := clampInt(position-max(0, (rows-height)/2), 0, total-rows)
	old := i.cache
	if old.width == width && old.background == background && old.top == top && old.height == rows {
		return old, nil
	}
	pixels := make([]uint32, width*rows)
	start, end := top, top
	if old.width == width && old.background == background {
		start, end = max(top, old.top), min(top+rows, old.top+old.height)
		if start < end {
			copy(pixels[(start-top)*width:(end-top)*width], old.pixels[(start-old.top)*width:(end-old.top)*width])
		} else {
			start, end = top, top
		}
	}
	for _, span := range [][2]int{{top, start}, {end, top + rows}} {
		for y := span[0]; y < span[1]; y++ {
			sy := int(int64(y) * int64(i.height) / int64(total))
			if sy >= i.height {
				sy = i.height - 1
			}
			row := pixels[(y-top)*width:]
			for x := 0; x < width; x++ {
				sx := int(int64(x) * int64(i.width) / int64(width))
				p := i.rgba[(sy*i.width+sx)*4:]
				a := uint32(p[3])
				r := (uint32(p[0])*a + ((background>>16)&255)*(255-a)) / 255
				g := (uint32(p[1])*a + ((background>>8)&255)*(255-a)) / 255
				b := (uint32(p[2])*a + (background&255)*(255-a)) / 255
				row[x] = 0xff000000 | r<<16 | g<<8 | b
			}
		}
	}
	i.cache = regionBitmap{pixels: pixels, width: width, height: rows, top: top, contentHeight: total,
		background: background, sourceWidth: i.width, sourceHeight: i.height}
	return i.cache, nil
}
