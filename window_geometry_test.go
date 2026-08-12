package main

import (
	"testing"

	"st-go/term"
)

func TestResolveWindowRect(t *testing.T) {
	px := func(v float64) term.GeometryValue { return term.GeometryValue{Value: v} }
	ratio := func(v float64) term.GeometryValue {
		return term.GeometryValue{Unit: term.GeometryRatio, Value: v}
	}
	cases := []struct {
		anchor     string
		x, y, w, h term.GeometryValue
		want       windowRect
	}{
		{"absolute", px(100), px(80), px(640), px(480), windowRect{100, 80, 640, 480}},
		{"top-left", px(8), px(10), ratio(.5), ratio(.5), windowRect{8, 10, 960, 540}},
		{"top", px(5), px(7), px(400), px(200), windowRect{765, 7, 400, 200}},
		{"top-right", px(8), px(10), px(400), px(200), windowRect{1512, 10, 400, 200}},
		{"right", px(8), px(10), px(400), px(200), windowRect{1512, 450, 400, 200}},
		{"bottom-right", px(8), px(10), px(400), px(200), windowRect{1512, 870, 400, 200}},
		{"bottom", px(5), px(7), px(400), px(200), windowRect{765, 873, 400, 200}},
		{"bottom-left", px(8), px(10), ratio(.25), px(56), windowRect{8, 1014, 480, 56}},
		{"left", px(8), px(10), px(400), px(200), windowRect{8, 450, 400, 200}},
	}
	for _, tc := range cases {
		req := term.WindowGeometryRequest{Anchor: tc.anchor, X: tc.x, Y: tc.y, W: tc.w, H: tc.h}
		got, err := resolveWindowRect(req, 1920, 1080, 18, 30)
		if err != nil {
			t.Fatalf("%s: %v", tc.anchor, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %#v want %#v", tc.anchor, got, tc.want)
		}
	}
}
