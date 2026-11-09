package frontend

/*
#include <stdlib.h>
#include "pdf_bridge.h"
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"codeberg.org/go-fonts/liberation/liberationmonobold"
	"codeberg.org/go-fonts/liberation/liberationmonobolditalic"
	"codeberg.org/go-fonts/liberation/liberationmonoitalic"
	"codeberg.org/go-fonts/liberation/liberationmonoregular"
	"codeberg.org/go-fonts/liberation/liberationsansbold"
	"codeberg.org/go-fonts/liberation/liberationsansbolditalic"
	"codeberg.org/go-fonts/liberation/liberationsansitalic"
	"codeberg.org/go-fonts/liberation/liberationsansregular"
	"codeberg.org/go-fonts/liberation/liberationserifbold"
	"codeberg.org/go-fonts/liberation/liberationserifbolditalic"
	"codeberg.org/go-fonts/liberation/liberationserifitalic"
	"codeberg.org/go-fonts/liberation/liberationserifregular"
	"golang.org/x/sys/unix"
	"st-go/limits"
)

var pdfFonts struct {
	sync.Once
	files []*os.File // Poppler lazily opens these paths; retain until process exit.
}

// The minimal Poppler build omits fontconfig and otherwise only searches old
// Ghostscript Type1 paths. Register embedded, metrically compatible Latin fonts
// so ordinary PDFs need not embed Helvetica/Times/Courier or depend on the host.
// memfd gives Poppler a pathname without leaving temporary font files on disk.
func preparePDFFonts() {
	pdfFonts.Do(func() {
		if C.pdf_font_support() == 0 {
			return
		}
		fonts := []struct {
			names []string
			data  []byte
		}{
			{[]string{"Courier"}, liberationmonoregular.TTF},
			{[]string{"Courier-Bold"}, liberationmonobold.TTF},
			{[]string{"Courier-Oblique"}, liberationmonoitalic.TTF},
			{[]string{"Courier-BoldOblique"}, liberationmonobolditalic.TTF},
			{[]string{"Helvetica", "ZapfDingbats"}, liberationsansregular.TTF},
			{[]string{"Helvetica-Bold"}, liberationsansbold.TTF},
			{[]string{"Helvetica-Oblique"}, liberationsansitalic.TTF},
			{[]string{"Helvetica-BoldOblique"}, liberationsansbolditalic.TTF},
			{[]string{"Times-Roman", "Symbol"}, liberationserifregular.TTF},
			{[]string{"Times-Bold"}, liberationserifbold.TTF},
			{[]string{"Times-Italic"}, liberationserifitalic.TTF},
			{[]string{"Times-BoldItalic"}, liberationserifbolditalic.TTF},
		}
		for _, font := range fonts {
			fd, err := unix.MemfdCreate("st-go-pdf-font", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
			if err != nil {
				limits.Report("PDF fonts", "%v", err)
				return
			}
			file := os.NewFile(uintptr(fd), "st-go-pdf-font")
			if _, err := file.Write(font.data); err != nil {
				file.Close()
				limits.Report("PDF fonts", "%v", err)
				return
			}
			if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
				file.Close()
				limits.Report("PDF fonts", "%v", err)
				return
			}
			pdfFonts.files = append(pdfFonts.files, file)
			path := C.CString(fmt.Sprintf("/proc/self/fd/%d", fd))
			for _, name := range font.names {
				cname := C.CString(name)
				C.pdf_register_font(cname, path)
				C.free(unsafe.Pointer(cname))
			}
			C.free(unsafe.Pointer(path))
		}
	})
}
