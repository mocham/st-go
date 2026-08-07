/*
 * stb_image wrapper.
 *
 * stb_image is a single-header library; the implementation is emitted only
 * when STB_IMAGE_IMPLEMENTATION is defined, in exactly one translation unit.
 * This file is that translation unit. It is precompiled to stb_image.o by the
 * Makefile so the ~280 KB implementation is not recompiled on every build.
 *
 * It also exposes a tiny helper that decodes an in-memory image buffer to
 * unpremultiplied RGBA, for use from the Go frontend via cgo.
 */
#define STB_IMAGE_IMPLEMENTATION
#include "stb_image.h"

#include <stdlib.h>

/* Decode an image held in memory into RGBA. Returns a malloc'd buffer of
 * width*height*4 bytes (or NULL on failure). The caller must free() it. */
unsigned char *
stb_decode_rgba(const unsigned char *data, int len, int *width, int *height)
{
	int comp;
	return stbi_load_from_memory(data, len, width, height, &comp, 4);
}
