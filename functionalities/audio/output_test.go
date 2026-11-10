package audio

import "testing"

func TestALSAOutputLabelsDoNotConfuseSpeakerEarphoneAndOtherRoutes(t *testing.T) {
	for _, tc := range []struct {
		label string
		mode  int
	}{
		{"Built-in Speaker", 0}, {"Speaker", 0}, {"SPK", 0},
		{"Earphone", 1}, {"Headphone", 1}, {"Headphones", 1}, {"Headset", 1}, {"HP", 1},
		{"Speaker+Headphone", -1}, {"Off", -1}, {"HDMI", -1}, {"0", -1}, {"1", -1}, {"", -1},
	} {
		if mode := alsaOutputLabel(tc.label); mode != tc.mode {
			t.Errorf("%q: mode %d, want %d", tc.label, mode, tc.mode)
		}
	}
}

func TestSelectedOutputRejectsInvalidSelectorsWithoutProbingPlayback(t *testing.T) {
	for _, card := range []string{"-1", "32", "not a card"} {
		t.Run(card, func(t *testing.T) {
			t.Setenv("ST_GO_AUDIO_OUTPUT_CARD", card)
			if _, known := SelectedOutput(); known {
				t.Fatal("invalid card was accepted")
			}
		})
	}
	t.Setenv("ST_GO_AUDIO_OUTPUT_CARD", "")
	t.Setenv("ST_GO_AUDIO_OUTPUT_CONTROL", "not-a-real-st-go-output-selector")
	if _, known := SelectedOutput(); known {
		t.Fatal("unavailable selector was misreported as an output")
	}
}

func TestSetOutputRejectsInvalidModes(t *testing.T) {
	for _, mode := range []int{-1, 2, 99} {
		if SetOutput(mode) {
			t.Fatal("invalid output mode was accepted", mode)
		}
	}
}
