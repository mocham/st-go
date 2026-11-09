//go:build googlepinyin && cgo && linux

package pinyinengine

/*
#cgo CXXFLAGS: -std=c++11
#cgo LDFLAGS: ${SRCDIR}/../../../../third_party/googlepinyin/libpinyinime.a -lstdc++ -lm -pthread
#include <stdint.h>
#include <stdlib.h>
int gp_open(int fd, long length, int user_fd);
void gp_close(void);
int gp_lookup(const char*, unsigned, unsigned, uint16_t*, uint32_t*, uint32_t*, uint32_t*);
*/
import "C"

import (
	"fmt"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/tui/pinyin"
)

type Engine struct{ userFD int }

func Open() (*Engine, error) {
	dictionary, err := dictionaryBytes()
	if err != nil {
		return nil, err
	}
	system, err := unix.MemfdCreate("govim-pinyin-system", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	defer unix.Close(system)
	for data := dictionary; len(data) > 0; {
		n, err := unix.Write(system, data)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return nil, fmt.Errorf("short dictionary write")
		}
		data = data[n:]
	}
	user, err := unix.MemfdCreate("govim-pinyin-user", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	if C.gp_open(C.int(system), C.long(len(dictionary)), C.int(user)) != 1 {
		unix.Close(user)
		return nil, fmt.Errorf("cannot initialize embedded Google Pinyin dictionary")
	}
	return &Engine{user}, nil
}
func (e *Engine) Close() { C.gp_close(); unix.Close(e.userFD) }
func (e *Engine) Lookup(r pinyin.Request) (pinyin.Response, error) {
	response := pinyin.Response{ID: r.ID, Prompt: r.Prompt}
	if err := r.Validate(); err != nil {
		return response, err
	}
	prompt := C.CString(r.Prompt)
	defer C.free(unsafe.Pointer(prompt))
	var text [5 * 128]C.uint16_t
	var lengths [5]C.uint32_t
	var total, page C.uint32_t
	count := int(C.gp_lookup(prompt, C.uint(len(r.Prompt)), C.uint(r.Page), &text[0], &lengths[0], &total, &page))
	if count < 0 || count > 5 {
		return response, fmt.Errorf("native Pinyin lookup failed")
	}
	response.Total, response.Page = int(total), int(page)
	for i := 0; i < count; i++ {
		n := int(lengths[i])
		if n < 1 || n >= 128 {
			return response, fmt.Errorf("invalid native candidate length")
		}
		units := make([]uint16, n)
		for j := range units {
			units[j] = uint16(text[i*128+j])
		}
		response.Candidates = append(response.Candidates, string(utf16.Decode(units)))
	}
	return response, response.Validate(r)
}
