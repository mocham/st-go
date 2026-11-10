//go:build !alsa_static

package audio

/*
#cgo pkg-config: alsa
*/
import "C"
