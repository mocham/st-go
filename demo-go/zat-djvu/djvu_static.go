//go:build djvu_static

package main

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/djvu/include
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/djvu/lib -ldjvulibre -L${SRCDIR}/../../third_party/jpeg/lib -ljpeg -lstdc++ -lpthread -lm
#include <st_go_djvu_static.h>
#ifndef ST_GO_DJVU_STATIC_SHA256
#error missing static DjVuLibre archive identity
#endif
*/
import "C"
