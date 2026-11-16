//go:build linux && cgo && ffmpeg_static && !purego

package main

/*
#cgo CFLAGS: -std=c11 -D_GNU_SOURCE -Wall -Wextra
#cgo CFLAGS: -I${SRCDIR}/../../third_party/libdrm/include -I${SRCDIR}/../../third_party/libdrm/include/libdrm
#cgo LDFLAGS: ${SRCDIR}/../../third_party/libdrm/lib/libdrm.a
#include <stdlib.h>
#include <st_go_drm_static.h>
#include "gov2_drm.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

type nativeScanout struct{ handle *C.gov_drm }

func drmError(operation string, code C.int) error {
	if code >= 0 {
		return nil
	}
	return fmt.Errorf("DRM %s: %w", operation, syscall.Errno(-code))
}

func openScanout(device string, connector uint32) (scanout, error) {
	paths := []string{device}
	if device == "" {
		paths, _ = filepath.Glob("/dev/dri/card[0-9]*")
	}
	var errs []error
	for _, path := range paths {
		name := C.CString(path)
		var code C.int
		h := C.gov_drm_open(name, C.uint32_t(connector), &code)
		C.free(unsafe.Pointer(name))
		if h != nil {
			return &nativeScanout{h}, nil
		}
		errs = append(errs, drmError(path, code))
	}
	return nil, fmt.Errorf("no usable active DRM output (requires a KMS card with dumb buffers and DRM master): %w", errors.Join(append(errs, syscall.ENODEV)...))
}

func (d *nativeScanout) back() ([]uint32, int, int, int, error) {
	var w, h, stride, code C.int
	p := C.gov_drm_back(d.handle, &w, &h, &stride, &code)
	if err := drmError("retire page flip", code); err != nil {
		return nil, 0, 0, 0, err
	}
	return unsafe.Slice((*uint32)(unsafe.Pointer(p)), int(stride)*int(h)), int(w), int(h), int(stride), nil
}
func (d *nativeScanout) flip() error    { return drmError("page flip", C.gov_drm_flip(d.handle)) }
func (d *nativeScanout) suspend() error { return drmError("release", C.gov_drm_suspend(d.handle)) }
func (d *nativeScanout) resume() error  { return drmError("acquire", C.gov_drm_resume(d.handle)) }
func (d *nativeScanout) close() error {
	if d.handle == nil {
		return nil
	}
	err := drmError("restore", C.gov_drm_close(d.handle))
	d.handle = nil
	return err
}
