//go:build linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || sparc64)

package audio

type alsaLong = int64
type alsaULong = uint64

const (
	alsaLongBytes = 8
	maxALSALong   = int64(^uint64(0) >> 1)
)
