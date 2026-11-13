package main

import "testing"

func TestSystemControlsUseHardwareReadbackAndRawBrightness(t *testing.T) {
	volume, light := 67, 400
	var volumeWrites, brightnessDeltas []int
	c := &systemControls{
		volume:     func() (int, bool) { return volume, true },
		setVolume:  func(next int) bool { volumeWrites = append(volumeWrites, next); volume = next; return true },
		brightness: func() (int, int, bool) { return light, 1000, true },
		changeBrightness: func(delta int) (int, bool) {
			brightnessDeltas = append(brightnessDeltas, delta)
			light += delta
			return light, true
		},
	}
	if s := c.poll(); s.volume != 67 || s.brightness != 400 {
		t.Fatalf("initial hardware state: %+v", s)
	}
	for _, action := range []string{"vol+", "vol-", "bright+", "bright-"} {
		if err := c.change(action); err != nil {
			t.Fatal(err)
		}
	}
	if len(volumeWrites) != 2 || volumeWrites[0] != 70 || volumeWrites[1] != 66 {
		t.Fatalf("volume steps: %v", volumeWrites)
	}
	if len(brightnessDeltas) != 2 || brightnessDeltas[0] != 5 || brightnessDeltas[1] != -4 || light != 401 {
		t.Fatalf("raw brightness steps: %v, %d", brightnessDeltas, light)
	}
	volume = 91 // A separate mixer changed the hardware between polls.
	if err := c.change("vol+"); err != nil || volume != 94 {
		t.Fatalf("external volume change ignored: %d %v", volume, err)
	}
	if err := c.change("mute"); err != nil || volume != 0 {
		t.Fatalf("mute: %d %v", volume, err)
	}
	if err := c.change("mute"); err != nil || volume != 94 {
		t.Fatalf("restore actual previous volume: %d %v", volume, err)
	}
}

func TestSystemControlsAccumulateQuantizedStepsAndReportFailures(t *testing.T) {
	actual := 0
	succeed := true
	known := true
	c := &systemControls{
		volume: func() (int, bool) { return actual, known },
		setVolume: func(next int) bool {
			if succeed {
				actual = next / 10 * 10
			}
			return succeed
		},
		brightness:       func() (int, int, bool) { return 0, 0, false },
		changeBrightness: func(int) (int, bool) { return 0, false },
	}
	if err := c.change("mute"); err == nil {
		t.Fatal("guessed an unmute volume without a previous level")
	}
	for range 4 {
		if err := c.change("vol+"); err != nil {
			t.Fatal(err)
		}
		c.poll()
	}
	if s := c.poll(); actual != 10 || s.volume != 12 {
		t.Fatalf("quantization lost repeated steps: actual %d displayed %d", actual, s.volume)
	}
	succeed = false
	if err := c.change("vol+"); err == nil {
		t.Fatal("failed mixer operation reported success")
	}
	if s := c.poll(); s.volume != 12 {
		t.Fatal("failed operation advanced display", s.volume)
	}
	if err := c.change("bright+"); err == nil {
		t.Fatal("failed brightness operation reported success")
	}
	known = false
	c.setVolume = func(int) bool { t.Fatal("write attempted without known hardware volume"); return false }
	if err := c.change("vol+"); err == nil {
		t.Fatal("unavailable mixer reported success")
	}
	if s := c.poll(); s.volume != -1 || s.brightness != -1 {
		t.Fatalf("unknown hardware state: %+v", s)
	}
}
