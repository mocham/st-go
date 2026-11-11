//go:build !djvu_static

package main

/*
#cgo pkg-config: ddjvuapi
#cgo LDFLAGS: -lm
*/
import "C"
