package main

import (
	"fmt"
	"image"
	"math/bits"

	"github.com/jezek/xgb/xproto"
)

type pixelFormat struct {
	depth, bpp, pad  byte
	msb              bool
	red, green, blue uint32
}

func newPixelFormat(setup *xproto.SetupInfo, screen *xproto.ScreenInfo) (pixelFormat, error) {
	p := pixelFormat{depth: screen.RootDepth, msb: setup.ImageByteOrder == xproto.ImageOrderMSBFirst}
	for _, f := range setup.PixmapFormats {
		if f.Depth == p.depth {
			p.bpp = f.BitsPerPixel
			p.pad = f.ScanlinePad
		}
	}
	for _, d := range screen.AllowedDepths {
		for _, v := range d.Visuals {
			if v.VisualId == screen.RootVisual && v.Class == xproto.VisualClassTrueColor {
				p.red, p.green, p.blue = v.RedMask, v.GreenMask, v.BlueMask
			}
		}
	}
	if p.red == 0 || p.green == 0 || p.blue == 0 || p.bpp != 16 && p.bpp != 24 && p.bpp != 32 || p.pad == 0 {
		return p, fmt.Errorf("gxw needs a 16/24/32-bit TrueColor X screen")
	}
	return p, nil
}

func component(v byte, mask uint32) uint32 {
	shift := bits.TrailingZeros32(mask)
	return (uint32(v)*(mask>>shift) + 127) / 255 << shift
}
func (p pixelFormat) pixel(r, g, b byte) uint32 {
	return component(r, p.red) | component(g, p.green) | component(b, p.blue)
}
func (p pixelFormat) stride(width int) int {
	return (width*int(p.bpp) + int(p.pad) - 1) / int(p.pad) * int(p.pad) / 8
}
func (p pixelFormat) encode(img *image.RGBA) []byte {
	stride := p.stride(img.Rect.Dx())
	n := int(p.bpp) / 8
	data := make([]byte, stride*img.Rect.Dy())
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			i := img.PixOffset(x+img.Rect.Min.X, y+img.Rect.Min.Y)
			v := p.pixel(img.Pix[i], img.Pix[i+1], img.Pix[i+2])
			off := y*stride + x*n
			for b := 0; b < n; b++ {
				shift := b * 8
				if p.msb {
					shift = (n - 1 - b) * 8
				}
				data[off+b] = byte(v >> shift)
			}
		}
	}
	return data
}
func (p pixelFormat) decode(data []byte, width, height int) (*image.RGBA, error) {
	stride := p.stride(width)
	n := int(p.bpp) / 8
	if len(data) < stride*height {
		return nil, fmt.Errorf("short X image: %d bytes", len(data))
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	channel := func(v, mask uint32) byte {
		shift := bits.TrailingZeros32(mask)
		return byte(((v & mask) >> shift) * 255 / (mask >> shift))
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var v uint32
			off := y*stride + x*n
			for b := 0; b < n; b++ {
				shift := b * 8
				if p.msb {
					shift = (n - 1 - b) * 8
				}
				v |= uint32(data[off+b]) << shift
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = channel(v, p.red), channel(v, p.green), channel(v, p.blue), 255
		}
	}
	return img, nil
}

func (w *wm) upload(id xproto.Window, img *image.RGBA) {
	data := w.pixels.encode(img)
	stride := w.pixels.stride(img.Rect.Dx())
	rows := max(1, (int(xproto.Setup(w.conn).MaximumRequestLength)*4-28)/stride)
	for y := 0; y < img.Rect.Dy(); y += rows {
		n := min(rows, img.Rect.Dy()-y)
		xproto.PutImage(w.conn, xproto.ImageFormatZPixmap, xproto.Drawable(id), w.gc, uint16(img.Rect.Dx()), uint16(n), 0, int16(y), 0, w.pixels.depth, data[y*stride:(y+n)*stride])
	}
}
