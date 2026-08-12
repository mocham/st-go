/* dummy-webp.c - stub for libwebp when WebP support is dropped.
 *
 * Provides the same symbols as libwebp's decode API (WebPGetInfo,
 * WebPDecodeRGBA, WebPFree) and the animated decoder (webp_anim_open,
 * webp_anim_next, webp_anim_delete) that webp_cgo.go / webp_anim_cgo.go call,
 * but returns failure so decodeWebP()/decodeWebPAnim() degrade gracefully (no
 * image, no animation) instead of crashing. Compiled to dummy-webp.o and
 * linked in place of libwebp.a + libwebpdemux.a for the st-min/st-stb
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

/* animated WebP: WebPAnimDecoder stubs (no animation in reduced builds) */
void *
webp_anim_open(const uint8_t *data, size_t len,
               int *width, int *height, int *frame_count)
{
	(void)data;
	(void)len;
	if (width)
		*width = 0;
	if (height)
		*height = 0;
	if (frame_count)
		*frame_count = 0;
	return NULL; /* failure */
}

int
webp_anim_next(void *handle, uint32_t *timestamp_ms, uint8_t **buf)
{
	(void)handle;
	if (timestamp_ms)
		*timestamp_ms = 0;
	if (buf)
		*buf = NULL;
	return 0; /* no frames */
}

void
webp_anim_delete(void *handle)
{
	(void)handle; /* no-op */
}
