#include <ft2build.h>
#include FT_FREETYPE_H
#include <stdlib.h>
#include <string.h>
#include <limits.h>
#include "font_freetype.h"

struct st_font_face {
	FT_Library library;
	FT_Face face;
	unsigned char *data;
};

void st_font_close(st_font_face *font) {
	if (!font) return;
	if (font->face) FT_Done_Face(font->face);
	if (font->library) FT_Done_FreeType(font->library);
	free(font->data);
	free(font);
}

st_font_face *st_font_open(const unsigned char *data, size_t len, int pixels) {
	if (!data || !len || len > LONG_MAX || pixels <= 0) return NULL;
	st_font_face *font = calloc(1, sizeof(*font));
	if (!font) return NULL;
	font->data = malloc(len);
	if (!font->data) { st_font_close(font); return NULL; }
	memcpy(font->data, data, len);
	if (FT_Init_FreeType(&font->library) ||
	    FT_New_Memory_Face(font->library, font->data, (FT_Long)len, 0, &font->face) ||
	    FT_Set_Pixel_Sizes(font->face, 0, pixels)) {
		st_font_close(font);
		return NULL;
	}
	return font;
}

int st_font_glyph(st_font_face *font, uint32_t codepoint, int max_pixels,
                  int *width, int *height, int *top) {
	if (!font || max_pixels < 1) return -1;
	FT_UInt index = FT_Get_Char_Index(font->face, codepoint);
	if (!index) return 0;
	if (FT_Load_Glyph(font->face, index, FT_LOAD_DEFAULT)) return -1;
	FT_GlyphSlot glyph = font->face->glyph;
	/* Bound prospective ink before asking FreeType to allocate its bitmap. */
	FT_Pos w = glyph->metrics.width / 64 + 2;
	FT_Pos h = glyph->metrics.height / 64 + 2;
	if (w < 1 || h < 1 || w > max_pixels || h > max_pixels / w) return -1;
	if (FT_Render_Glyph(glyph, FT_RENDER_MODE_NORMAL)) return -1;
	FT_Bitmap *bitmap = &glyph->bitmap;
	if (bitmap->width > INT_MAX || bitmap->rows > INT_MAX ||
	    (bitmap->width && bitmap->rows > (unsigned)max_pixels / bitmap->width)) return -1;
	if (bitmap->width && bitmap->rows && bitmap->pixel_mode != FT_PIXEL_MODE_GRAY &&
	    bitmap->pixel_mode != FT_PIXEL_MODE_MONO) return -1;
	*width = bitmap->width;
	*height = bitmap->rows;
	*top = glyph->bitmap_top;
	return 1;
}

int st_font_copy_mask(st_font_face *font, unsigned char *dst, size_t len) {
	if (!font || !dst) return 0;
	FT_Bitmap *bitmap = &font->face->glyph->bitmap;
	if ((size_t)bitmap->width * bitmap->rows != len || !bitmap->buffer) return 0;
	for (unsigned y = 0; y < bitmap->rows; y++) {
		const unsigned char *row = bitmap->buffer + (ptrdiff_t)y * bitmap->pitch;
		for (unsigned x = 0; x < bitmap->width; x++) {
			unsigned char a;
			if (bitmap->pixel_mode == FT_PIXEL_MODE_MONO)
				a = (row[x / 8] & (0x80 >> (x % 8))) ? 255 : 0;
			else if (bitmap->num_grays == 256) a = row[x];
			else if (bitmap->num_grays > 1) a = (unsigned)row[x] * 255 / (bitmap->num_grays - 1);
			else return 0;
			dst[(size_t)y * bitmap->width + x] = a;
		}
	}
	return 1;
}
