package audio

import "testing"

func TestSelectedOutputRejectsInvalidCardsWithoutProbingPlayback(t *testing.T) {
	for _, card := range []string{"-1", "32", "not a card"} {
		t.Run(card, func(t *testing.T) {
			t.Setenv("ST_GO_AUDIO_OUTPUT_CARD", card)
			if _, known := SelectedOutput(); known {
				t.Fatal("invalid card was accepted")
			}
		})
	}
}

func TestSetOutputRejectsInvalidModes(t *testing.T) {
	for _, mode := range []int{-1, 2, 99} {
		if SetOutput(mode) {
			t.Fatal("invalid output mode was accepted", mode)
		}
	}
}

func TestSetVolumeRejectsInvalidPercentages(t *testing.T) {
	for _, percent := range []int{-1, 101, 999} {
		if SetVolume(percent) {
			t.Fatal("invalid volume was accepted", percent)
		}
	}
}
