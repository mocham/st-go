package api

import "testing"

func TestNativeVideoPlayerSelection(t *testing.T) {
	base := NativeVideoRequest{Player: "gov2", File: "/media/movie '; 世界.mp4", Volume: 100, Muted: true, DRMDevice: "/dev/dri/card0", Connector: 42}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*NativeVideoRequest){
		func(r *NativeVideoRequest) { r.Player = "/bin/sh" },
		func(r *NativeVideoRequest) { r.File = "relative.mp4" },
		func(r *NativeVideoRequest) { r.Volume = -1 },
		func(r *NativeVideoRequest) { r.DRMDevice = "/dev/dri/card0/../../mem" },
		func(r *NativeVideoRequest) { r.DRMDevice = "/dev/dri/renderD128" },
		func(r *NativeVideoRequest) { r.ALSAConfig = "/etc/asound.conf" },
		func(r *NativeVideoRequest) { r.AudioDevice = "alsa/default" },
	} {
		r := base
		change(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("accepted invalid request", r)
		}
	}
	legacy := NativeVideoRequest{File: base.File, ALSAConfig: "/private/alsa.conf", AudioDevice: "alsa/plughw:1,0", Volume: 33}
	if err := legacy.Validate(); err != nil {
		t.Fatal("broke legacy mpv handoff", err)
	}
}
