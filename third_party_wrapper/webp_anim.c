/*
 * webp_anim.c — animated-WebP frame decoder for the Go file-browser, built on
 * libwebp's WebPAnimDecoder (from libwebpdemux). It exposes a minimal,
 * header-free API to the cgo Go side: open returns the canvas size and frame
 * count, next() yields each composited RGBA frame with its timestamp (ms),
 * delete frees the decoder.
 *
 * Compiled once (like alsa_snd.o) so the Go side only declares extern
 * functions and never includes libwebp headers.
 */
#include <webp/demux.h>
#include <stdlib.h>
#include <stdint.h>

typedef struct webp_anim {
	WebPAnimDecoder *dec;
	uint32_t timestamp;
	int width;
	int height;
	int frame_count;
	int has_next;
} webp_anim_t;

/* Reads canvas dimensions and per-frame durations without rasterizing any
 * pixels. The caller owns *durations and frees it with free(). */
int webp_anim_info(const uint8_t *data, size_t len, int *width, int *height,
		   uint32_t **durations) {
	WebPData wd;
	WebPDemuxer *demux;
	WebPIterator iter;
	uint32_t *out;
	int frame_count;
	int i;

	wd.bytes = data;
	wd.size = len;
	demux = WebPDemux(&wd);
	if (demux == NULL)
		return 0;

	frame_count = (int)WebPDemuxGetI(demux, WEBP_FF_FRAME_COUNT);
	if (frame_count <= 0 || !WebPDemuxGetFrame(demux, 1, &iter)) {
		WebPDemuxDelete(demux);
		return 0;
	}
	out = malloc((size_t)frame_count * sizeof(*out));
	if (out == NULL) {
		WebPDemuxReleaseIterator(&iter);
		WebPDemuxDelete(demux);
		return 0;
	}

	for (i = 0; i < frame_count; i++) {
		out[i] = (uint32_t)iter.duration;
		if (i + 1 < frame_count && !WebPDemuxNextFrame(&iter)) {
			free(out);
			WebPDemuxReleaseIterator(&iter);
			WebPDemuxDelete(demux);
			return 0;
		}
	}
	if (width)
		*width = (int)WebPDemuxGetI(demux, WEBP_FF_CANVAS_WIDTH);
	if (height)
		*height = (int)WebPDemuxGetI(demux, WEBP_FF_CANVAS_HEIGHT);
	if (durations)
		*durations = out;
	else
		free(out);
	WebPDemuxReleaseIterator(&iter);
	WebPDemuxDelete(demux);
	return frame_count;
}

void *webp_anim_open(const uint8_t *data, size_t len,
		     int *width, int *height, int *frame_count) {
	WebPData wd;
	wd.bytes = data;
	wd.size = len;

	WebPAnimDecoderOptions opts;
	WebPAnimDecoderOptionsInit(&opts);
	WebPAnimDecoder *dec = WebPAnimDecoderNew(&wd, &opts);
	if (dec == NULL)
		return NULL;

	WebPAnimInfo info;
	if (!WebPAnimDecoderGetInfo(dec, &info)) {
		WebPAnimDecoderDelete(dec);
		return NULL;
	}

	webp_anim_t *a = malloc(sizeof(webp_anim_t));
	if (a == NULL) {
		WebPAnimDecoderDelete(dec);
		return NULL;
	}
	a->dec = dec;
	a->timestamp = 0;
	a->width = (int)info.canvas_width;
	a->height = (int)info.canvas_height;
	a->frame_count = (int)info.frame_count;
	a->has_next = WebPAnimDecoderHasMoreFrames(dec);

	if (width) *width = a->width;
	if (height) *height = a->height;
	if (frame_count) *frame_count = a->frame_count;
	return a;
}

/* Returns 1 on a frame (buf is a width*height*4 RGBA buffer, timestamp_ms in
 * ms from the animation start), 0 at the end. */
int webp_anim_next(void *handle, uint32_t *timestamp_ms, uint8_t **buf) {
	webp_anim_t *a = (webp_anim_t *)handle;
	if (a == NULL || !a->has_next)
		return 0;
	if (!WebPAnimDecoderGetNext(a->dec, buf, &a->timestamp))
		return 0;
	if (timestamp_ms) *timestamp_ms = a->timestamp;
	a->has_next = WebPAnimDecoderHasMoreFrames(a->dec);
	return 1;
}

void webp_anim_delete(void *handle) {
	webp_anim_t *a = (webp_anim_t *)handle;
	if (a) {
		if (a->dec)
			WebPAnimDecoderDelete(a->dec);
		free(a);
	}
}
