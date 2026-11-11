//go:build linux

package audio

import (
	"runtime"
	"structs"
	"unsafe"
)

const (
	pcmParamAccess        = 0
	pcmParamFormat        = 1
	pcmParamSubformat     = 2
	pcmParamChannels      = 10
	pcmParamRate          = 11
	pcmParamPeriodSize    = 13
	pcmParamPeriods       = 15
	pcmParamBufferSize    = 17
	pcmFirstInterval      = 8
	pcmLastInterval       = 19
	pcmAccessRWInterleave = 3
	pcmFormatS16LE        = 2

	intervalOpenMin = 1 << 0
	intervalOpenMax = 1 << 1
	intervalInteger = 1 << 2
	intervalEmpty   = 1 << 3

	ctlElementBoolean = 1
	ctlElementInteger = 2
	ctlInterfaceMixer = 2
	ctlAccessRead     = 1 << 0
	ctlAccessWrite    = 1 << 1

	ctlValueBytes = 128 * alsaLongBytes
)

type pcmMask struct {
	_    structs.HostLayout
	Bits [8]uint32
}

type pcmInterval struct {
	_               structs.HostLayout
	Min, Max, Flags uint32
}

type pcmHWParams struct {
	_         structs.HostLayout
	Flags     uint32
	Masks     [3]pcmMask
	MaskRes   [5]pcmMask
	Intervals [pcmLastInterval - pcmFirstInterval + 1]pcmInterval
	IntRes    [9]pcmInterval
	RMask     uint32
	CMask     uint32
	Info      uint32
	MSBits    uint32
	RateNum   uint32
	RateDen   uint32
	FIFOSize  alsaULong
	Sync      [16]byte
	Reserved  [48]byte
}

type pcmSWParams struct {
	_                structs.HostLayout
	TimestampMode    uint32
	PeriodStep       uint32
	SleepMin         uint32
	AvailMin         alsaULong
	TransferAlign    alsaULong
	StartThreshold   alsaULong
	StopThreshold    alsaULong
	SilenceThreshold alsaULong
	SilenceSize      alsaULong
	Boundary         alsaULong
	Protocol         uint32
	TimestampType    uint32
	Reserved         [56]byte
}

type pcmXfer struct {
	_      structs.HostLayout
	Result alsaLong
	Buffer uintptr
	Frames alsaULong
}

type ctlElementID struct {
	_         structs.HostLayout
	Number    uint32
	Interface int32
	Device    uint32
	Subdevice uint32
	Name      [44]byte
	Index     uint32
}

type ctlElementList struct {
	_        structs.HostLayout
	Offset   uint32
	Space    uint32
	Used     uint32
	Count    uint32
	IDs      uintptr
	Reserved [50]byte
}

type ctlInfoValue struct {
	_    [0]int64 // Give the C union its host int64 alignment.
	Data [128]byte
}

type ctlElementInfo struct {
	_        structs.HostLayout
	ID       ctlElementID
	Type     int32
	Access   uint32
	Count    uint32
	Owner    int32
	Value    ctlInfoValue
	Reserved [64]byte
}

type ctlElementValueData struct {
	_    [0]alsaLong // C integer controls are long[], not int32[] on 64-bit.
	Data [ctlValueBytes]byte
}

type ctlElementValue struct {
	_        structs.HostLayout
	ID       ctlElementID
	Indirect uint32
	Value    ctlElementValueData
	Reserved [128]byte
}

func controlLongs(value *ctlElementValue, count int) []alsaLong {
	return unsafe.Slice((*alsaLong)(unsafe.Pointer(&value.Value.Data[0])), count)
}

func controlInfoLongs(info *ctlElementInfo) []alsaLong {
	return unsafe.Slice((*alsaLong)(unsafe.Pointer(&info.Value.Data[0])), 3)
}

func ioctlReadDirection() uintptr {
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "sparc64":
		return 1
	default:
		return 2
	}
}

func ioctlWriteDirection() uintptr {
	if ioctlReadDirection() == 1 {
		return 2
	}
	return 1
}

func ioctlRequest(direction, kind, number, size uintptr) uintptr {
	return direction<<30 | size<<16 | kind<<8 | number
}

func ioctlNone(kind, number uintptr) uintptr {
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "sparc64":
		// These ABIs use a three-bit direction field at bit 29 and encode
		// _IOC_NONE as one, unlike asm-generic's zero value.
		return 1<<29 | kind<<8 | number
	}
	return ioctlRequest(0, kind, number, 0)
}

func ioctlWrite(kind, number, size uintptr) uintptr {
	return ioctlRequest(ioctlWriteDirection(), kind, number, size)
}

func ioctlReadWrite(kind, number, size uintptr) uintptr {
	return ioctlRequest(ioctlReadDirection()|ioctlWriteDirection(), kind, number, size)
}

var (
	pcmIOCTLHWRefine = ioctlReadWrite('A', 0x10, unsafe.Sizeof(pcmHWParams{}))
	pcmIOCTLHWParams = ioctlReadWrite('A', 0x11, unsafe.Sizeof(pcmHWParams{}))
	pcmIOCTLSWParams = ioctlReadWrite('A', 0x13, unsafe.Sizeof(pcmSWParams{}))
	pcmIOCTLPrepare  = ioctlNone('A', 0x40)
	pcmIOCTLDrop     = ioctlNone('A', 0x43)
	pcmIOCTLDrain    = ioctlNone('A', 0x44)
	pcmIOCTLWrite    = ioctlWrite('A', 0x50, unsafe.Sizeof(pcmXfer{}))

	ctlIOCTLElementList  = ioctlReadWrite('U', 0x10, unsafe.Sizeof(ctlElementList{}))
	ctlIOCTLElementInfo  = ioctlReadWrite('U', 0x11, unsafe.Sizeof(ctlElementInfo{}))
	ctlIOCTLElementRead  = ioctlReadWrite('U', 0x12, unsafe.Sizeof(ctlElementValue{}))
	ctlIOCTLElementWrite = ioctlReadWrite('U', 0x13, unsafe.Sizeof(ctlElementValue{}))
)
