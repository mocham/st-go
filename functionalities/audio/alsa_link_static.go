//go:build alsa_static

package audio

/*
#cgo LDFLAGS: ${SRCDIR}/../../third_party/alsa/snd.o -L${SRCDIR}/../../third_party/alsa/lib -l:libasound.a -ldl -lpthread -lm
*/
import "C"
