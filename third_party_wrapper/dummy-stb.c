/* dummy-stb.c - stub for stb_image when stb support is dropped.
 *
 * Provides the same C symbols as third_party_wrapper/stb_image.c
 * (which emits the stb_image implementation), but returns failure so the
 * Go decodeImage() path degrades gracefully (no image shown) instead of
 * crashing. Compiled to dummy-stb.o and linked in place of stb_image.o
 * for the st-min target.
 */
#include <stdlib.h>

/* stb_image's decode helper (see img_cgo.go). Always fail: no image. */
unsigned char *
stb_decode_rgba(const unsigned char *data, int len, int *width, int *height)
{
	(void)data;
	(void)len;
	if (width)
		*width = 0;
	if (height)
		*height = 0;
	return NULL;
}
