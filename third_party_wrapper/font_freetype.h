#ifndef ST_GO_FONT_FREETYPE_H
#define ST_GO_FONT_FREETYPE_H

#include <stddef.h>
#include <stdint.h>

/* A face owns its FreeType library and a copy of its input bytes. No Go pointer
 * or borrowed file descriptor is retained. Calls on one face are serialized. */
typedef struct st_font_face st_font_face;
st_font_face *st_font_open(const unsigned char *data, size_t len, int pixels);
void st_font_close(st_font_face *font);
/* 1 = rendered (possibly empty), 0 = missing character, -1 = error. */
int st_font_glyph(st_font_face *font, uint32_t codepoint, int max_pixels,
                  int *width, int *height, int *top);
int st_font_copy_mask(st_font_face *font, unsigned char *dst, size_t len);

#endif
