/* dummy-webp.c - stub for libwebp when WebP support is dropped.
 *
 * Provides the same symbols as libwebp's decode API (WebPGetInfo,
 * WebPDecodeRGBA, WebPFree) that webp_cgo.go calls, but returns failure so
 * decodeWebP() degrades gracefully (no image) instead of crashing. Compiled
 * to dummy-webp.o and linked in place of libwebp.a for the st-min/st-stb
 * targets.
 */
#include <stddef.h>
#include <stdint.h>

int
WebPGetInfo(const uint8_t *data, size_t data_size, int *width, int *height)
{
	(void)data;
	(void)data_size;
	if (width)
		*width = 0;
	if (height)
		*height = 0;
	return 0; /* failure: not a WebP */
}

uint8_t *
WebPDecodeRGBA(const uint8_t *data, size_t data_size,
               int *width, int *height)
{
	(void)data;
	(void)data_size;
	if (width)
		*width = 0;
	if (height)
		*height = 0;
	return NULL; /* failure */
}

void
WebPFree(void *ptr)
{
	(void)ptr; /* no-op; nothing was allocated */
}
