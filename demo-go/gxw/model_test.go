package main

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

func TestDialogPolicyAndGeometry(t *testing.T) {
	clients := map[xproto.Window]*client{10: {id: 10}, 20: {id: 20, dialog: true, transient: 10}, 30: {id: 30, dialog: true, transient: 20}}
	for _, tt := range []struct {
		explicit bool
		parent   xproto.Window
		want     bool
	}{{false, 0, false}, {false, 1, false}, {false, 99, false}, {false, 40, false}, {false, 10, true}, {true, 0, true}} {
		if got := isDialog(tt.explicit, tt.parent, 40, 1, clients); got != tt.want {
			t.Fatalf("policy %+v: %v", tt, got)
		}
	}
	if got := family(30, clients); got != 10 {
		t.Fatalf("nested dialog family: %d", got)
	}
	delete(clients, 20)
	if got := family(30, clients); got != 30 {
		t.Fatalf("orphan family: %d", got)
	}
	area := image.Rect(0, 32, 1024, 736)
	h := sizeHints{min: image.Pt(100, 100), max: image.Pt(500, 400), base: image.Pt(4, 4), increment: image.Pt(8, 16)}
	r := dialogBounds(area, image.Pt(497, 389), h, 2)
	if r.Dx() != 492 || r.Dy() != 388 || !r.In(area) || r.Min.X != (area.Dx()-r.Dx()-4)/2 {
		t.Fatalf("dialog geometry: %v", r)
	}
	r = dialogBounds(area, image.Pt(20000, 20000), sizeHints{min: image.Pt(20000, 20000)}, 2)
	if r.Dx() != 1020 || r.Dy() != 700 || !r.In(area) {
		t.Fatalf("oversized dialog: %v", r)
	}
}

func TestOverflowTabsKeepActiveVisible(t *testing.T) {
	for _, width := range []int{80, 200, 640, 1920} {
		for active := 0; active < 37; active++ {
			widths := make([]int, 37)
			for i := range widths {
				widths[i] = 128
				if i%3 == 0 {
					widths[i] = 32
				}
			}
			start, end, _ := tabRange(widths, active, width, 32)
			if start > active || end <= active || start < 0 || end > 37 {
				t.Fatalf("width %d active %d: %d..%d", width, active, start, end)
			}
		}
	}
}

func TestTrueColorPixelFormats(t *testing.T) {
	for _, p := range []pixelFormat{
		{depth: 24, bpp: 32, pad: 32, red: 0xff0000, green: 0xff00, blue: 0xff},
		{depth: 24, bpp: 24, pad: 32, msb: true, red: 0xff0000, green: 0xff00, blue: 0xff},
		{depth: 16, bpp: 16, pad: 32, red: 0xf800, green: 0x7e0, blue: 0x1f},
		{depth: 16, bpp: 16, pad: 32, msb: true, red: 0x1f, green: 0x7e0, blue: 0xf800},
	} {
		img := image.NewRGBA(image.Rect(0, 0, 3, 2))
		colors := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}}
		for y := 0; y < 2; y++ {
			for x, c := range colors {
				img.SetRGBA(x, y, c)
			}
		}
		data := p.encode(img)
		decoded, err := p.decode(data, 3, 2)
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 2; y++ {
			for x, c := range colors {
				if got := decoded.RGBAAt(x, y); got != c {
					t.Fatalf("%+v: %v != %v", p, got, c)
				}
			}
		}
		if _, err := p.decode(data[:len(data)-1], 3, 2); err == nil {
			t.Fatal("accepted truncated image")
		}
	}
}

func TestNativeGoFontRendersWithoutFontFiles(t *testing.T) {
	f, err := newFont("", 16)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, 240, 32))
	draw.Draw(img, img.Rect, image.Black, image.Point{}, draw.Src)
	d := font.Drawer{Dst: img, Src: image.White, Face: f, Dot: fixed.P(4, 22)}
	d.DrawString("gxw  Café  Δ  123")
	ink := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			ink++
		}
	}
	if ink < 200 || f.Metrics().Height.Ceil() < 16 {
		t.Fatalf("missing font raster: %d pixels", ink)
	}
}
