//go:build linux

package audio

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestALSAKernelABILayout(t *testing.T) {
	type layout struct {
		hardware, software, transfer uintptr
		id, list, info, value        uintptr
		valueOffset                  uintptr
	}
	want := layout{608, 136, 24, 64, 80, 272, 1224, 72}
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = layout{604, 104, 12, 64, 72, 272, 708, 68}
	}
	got := layout{
		unsafe.Sizeof(pcmHWParams{}), unsafe.Sizeof(pcmSWParams{}), unsafe.Sizeof(pcmXfer{}),
		unsafe.Sizeof(ctlElementID{}), unsafe.Sizeof(ctlElementList{}), unsafe.Sizeof(ctlElementInfo{}), unsafe.Sizeof(ctlElementValue{}),
		unsafe.Offsetof(ctlElementValue{}.Value),
	}
	if got != want {
		t.Fatalf("Linux ALSA UAPI layout = %+v, want %+v", got, want)
	}
	if unsafe.Offsetof(ctlElementInfo{}.Value) != 80 {
		t.Fatalf("control info union offset = %d, want 80", unsafe.Offsetof(ctlElementInfo{}.Value))
	}
}

func TestALSAIOCTLNumbers(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("fixed values below document the amd64 Linux UAPI")
	}
	wants := map[string]struct{ got, want uintptr }{
		"HW_REFINE":  {pcmIOCTLHWRefine, 0xc2604110},
		"HW_PARAMS":  {pcmIOCTLHWParams, 0xc2604111},
		"SW_PARAMS":  {pcmIOCTLSWParams, 0xc0884113},
		"PREPARE":    {pcmIOCTLPrepare, 0x4140},
		"DROP":       {pcmIOCTLDrop, 0x4143},
		"DRAIN":      {pcmIOCTLDrain, 0x4144},
		"WRITEI":     {pcmIOCTLWrite, 0x40184150},
		"ELEM_LIST":  {ctlIOCTLElementList, 0xc0505510},
		"ELEM_INFO":  {ctlIOCTLElementInfo, 0xc1105511},
		"ELEM_READ":  {ctlIOCTLElementRead, 0xc4c85512},
		"ELEM_WRITE": {ctlIOCTLElementWrite, 0xc4c85513},
	}
	for name, values := range wants {
		if values.got != values.want {
			t.Errorf("%s request = %#x, want %#x", name, values.got, values.want)
		}
	}
}

func TestALSAControlValuesUseCLongWidth(t *testing.T) {
	var value ctlElementValue
	values := controlLongs(&value, 2)
	values[0], values[1] = 24, 29
	if values[0] != 24 || values[1] != 29 {
		t.Fatal("stereo control values overlapped")
	}
	first := unsafe.Pointer(&value.Value.Data[0])
	second := unsafe.Pointer(&value.Value.Data[alsaLongBytes])
	if uintptr(second)-uintptr(first) != alsaLongBytes {
		t.Fatal("control channels do not use C long spacing")
	}
}

func TestBoundedPCMAndMixerHelpers(t *testing.T) {
	params := initHWParams()
	if params.RMask != ^uint32(0) || params.Info != ^uint32(0) {
		t.Fatal("hardware parameter space was not initialized")
	}
	for i, interval := range params.Intervals {
		if interval.Min != 0 || interval.Max != ^uint32(0) || interval.Flags != 0 {
			t.Fatalf("interval %d was not initialized to the full range", i)
		}
	}
	for _, test := range []struct {
		minimum, maximum alsaLong
		percent          int
		want             alsaLong
		ok               bool
	}{
		{0, 74, 0, 0, true},
		{0, 74, 33, 24, true},
		{0, 74, 100, 74, true},
		{-10, 10, 50, 0, true},
		{0, 74, -1, 0, false},
		{10, 10, 50, 0, false},
	} {
		got, ok := controlPercentValue(test.minimum, test.maximum, test.percent)
		if got != test.want || ok != test.ok {
			t.Errorf("controlPercentValue(%d, %d, %d) = %d, %v; want %d, %v", test.minimum, test.maximum, test.percent, got, ok, test.want, test.ok)
		}
	}
}

func TestVolumeReadbackConversion(t *testing.T) {
	for _, tt := range []struct {
		min, max, value alsaLong
		want            int
		ok              bool
	}{
		{0, 74, 0, 0, true}, {0, 74, 24, 32, true}, {0, 74, 74, 100, true},
		{-10, 10, 0, 50, true}, {0, 31, 1, 3, true},
		{0, 74, 75, 0, false}, {0, 74, -1, 0, false}, {10, 10, 10, 0, false},
	} {
		got, ok := controlValuePercent(tt.min, tt.max, tt.value)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("volume readback %+v: %d, %v", tt, got, ok)
		}
	}
}
