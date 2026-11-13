package api

import (
	"math"
	"testing"
)

func TestVideoSpeedValidation(t *testing.T) {
	for _, speed := range []float64{0, -1, MinVideoSpeed - 0.01, MaxVideoSpeed + 0.01, math.NaN(), math.Inf(1)} {
		if (VideoRequest{Action: "speed", Speed: speed}).Validate() == nil {
			t.Fatalf("accepted speed %v", speed)
		}
	}
	for _, speed := range []float64{MinVideoSpeed, 1, 1.1, MaxVideoSpeed} {
		if err := (VideoRequest{Action: "speed", Speed: speed}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
