package main

/*
#include <stdlib.h>
#include "djvu_bridge.h"
*/
import "C"

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strings"
	"unsafe"

	"st-go/limits"
	"st-go/pdfmeta"
)

type djvuDocument struct {
	pointer *C.zat_djvu_document
	pages   int
}

type boundedPNGWriter struct {
	writer  io.Writer
	written int64
}

func (w *boundedPNGWriter) Write(data []byte) (int, error) {
	remaining := int64(limits.FileBytes) - w.written
	if remaining <= 0 {
		return 0, fmt.Errorf("encoded DjVu page exceeds %d bytes", limits.FileBytes)
	}
	overflow := int64(len(data)) > remaining
	if overflow {
		data = data[:remaining]
	}
	n, err := w.writer.Write(data)
	w.written += int64(n)
	if err == nil && overflow {
		err = fmt.Errorf("encoded DjVu page exceeds %d bytes", limits.FileBytes)
	}
	return n, err
}

func openDjVu(path string, background uint32) (*djvuDocument, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	pointer := C.zat_djvu_open(cpath, C.uint32_t(background))
	if pointer == nil {
		return nil, fmt.Errorf("could not allocate a DjVuLibre document")
	}
	document := &djvuDocument{pointer: pointer, pages: int(C.zat_djvu_pages(pointer))}
	if document.pages < 1 || document.pages > limits.PreviewPages {
		err := document.lastError()
		if document.pages > limits.PreviewPages {
			err = fmt.Sprintf("DjVu document exceeds the %d-page limit", limits.PreviewPages)
		}
		document.close()
		return nil, fmt.Errorf("%s", err)
	}
	return document, nil
}

func (d *djvuDocument) lastError() string {
	if d == nil || d.pointer == nil {
		return "DjVu document is closed"
	}
	message := C.GoString(C.zat_djvu_error(d.pointer))
	if message == "" {
		message = "DjVuLibre operation failed"
	}
	return message
}

func (d *djvuDocument) close() {
	if d != nil && d.pointer != nil {
		C.zat_djvu_close(d.pointer)
		d.pointer = nil
	}
}

func (d *djvuDocument) renderPNG(page int, path string) (int, int, error) {
	if d == nil || d.pointer == nil || page < 1 || page > d.pages {
		return 0, 0, fmt.Errorf("invalid DjVu page %d", page)
	}
	var pixels *C.uchar
	var width, height C.int
	maximum := C.uint64_t(limits.DecodedImageBytes / 4)
	if C.zat_djvu_render_page(d.pointer, C.int(page-1), maximum, &pixels, &width, &height) == 0 {
		return 0, 0, fmt.Errorf("%s", d.lastError())
	}
	defer C.zat_djvu_free(unsafe.Pointer(pixels))
	w, h := int(width), int(height)
	if w < 1 || h < 1 || int64(w)*int64(h)*4 > limits.DecodedImageBytes {
		return 0, 0, fmt.Errorf("DjVu page raster exceeds the decoded-image limit")
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(pixels)), w*h*4)
	image := &image.RGBA{Pix: data, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, 0, err
	}
	encoded := &boundedPNGWriter{writer: file}
	encodeErr := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(encoded, image)
	closeErr := file.Close()
	if encodeErr != nil {
		os.Remove(path)
		return 0, 0, encodeErr
	}
	if closeErr != nil {
		os.Remove(path)
		return 0, 0, closeErr
	}
	return w, h, nil
}

func (d *djvuDocument) pageText(page int) (string, bool, error) {
	if d == nil || d.pointer == nil || page < 1 || page > d.pages {
		return "", false, fmt.Errorf("invalid DjVu page %d", page)
	}
	buffer := make([]byte, pdfmeta.TextBytes+1)
	var length C.size_t
	var truncated C.int
	if C.zat_djvu_page_text(d.pointer, C.int(page-1), (*C.char)(unsafe.Pointer(&buffer[0])),
		C.size_t(len(buffer)), &length, &truncated) == 0 {
		return "", false, fmt.Errorf("%s", d.lastError())
	}
	n := int(length)
	if n < 0 || n > len(buffer)-1 {
		return "", false, fmt.Errorf("DjVuLibre returned invalid hidden text")
	}
	text := strings.ToValidUTF8(string(buffer[:n]), "")
	return text, truncated != 0, nil
}

func (d *djvuDocument) outline() ([]pdfmeta.Entry, bool, error) {
	if d == nil || d.pointer == nil {
		return nil, false, fmt.Errorf("DjVu document is closed")
	}
	outline := C.zat_djvu_get_outline(d.pointer, C.size_t(pdfmeta.MaxEntries), C.size_t(pdfmeta.MaxBytes))
	if outline == nil {
		return nil, false, fmt.Errorf("%s", d.lastError())
	}
	defer C.zat_djvu_outline_free(outline)
	count := int(C.zat_djvu_outline_count(outline))
	if count < 0 || count > pdfmeta.MaxEntries {
		return nil, false, fmt.Errorf("DjVuLibre returned an invalid outline")
	}
	entries := make([]pdfmeta.Entry, 0, count)
	for index := 0; index < count; index++ {
		length := int(C.zat_djvu_outline_title_length(outline, C.size_t(index)))
		title := C.zat_djvu_outline_title(outline, C.size_t(index))
		if title == nil || length < 0 || length > pdfmeta.MaxBytes {
			return nil, false, fmt.Errorf("DjVuLibre returned an invalid outline title")
		}
		entries = append(entries, pdfmeta.Entry{
			Title: strings.ToValidUTF8(C.GoStringN(title, C.int(length)), ""),
			Page:  int(C.zat_djvu_outline_page(outline, C.size_t(index))),
			Level: int(C.zat_djvu_outline_level(outline, C.size_t(index))),
		})
	}
	return entries, C.zat_djvu_outline_truncated(outline) != 0, nil
}
