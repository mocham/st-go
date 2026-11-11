//go:build linux && (386 || arm || mips || mipsle)

package audio

type alsaLong = int32
type alsaULong = uint32

const (
	alsaLongBytes = 4
	maxALSALong   = int32(^uint32(0) >> 1)
)
