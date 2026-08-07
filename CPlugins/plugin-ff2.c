#include <ft2build.h>
#include FT_FREETYPE_H
#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <limits.h>
#include <dirent.h>
#include <unistd.h>
#include "ff2.h"

uint32_t utf8_to_codepoint(const char* utf8) {
    unsigned char c = *(const unsigned char*)utf8;
    if (c < 0x80) return c;
    if (c < 0xE0) return ((c & 0x1F) << 6)  | (utf8[1] & 0x3F);
    if (c < 0xF0) return ((c & 0x0F) << 12) | ((utf8[1] & 0x3F) << 6) | (utf8[2] & 0x3F);
    return ((c & 0x07) << 18) | ((utf8[1] & 0x3F) << 12) | ((utf8[2] & 0x3F) << 6) | (utf8[3] & 0x3F);
}

static FT_Library ft_lib = NULL;
static FT_Face fonts[FF2_MAXFONTS + 1];

/* ------------------------------------------------------------------ */
/* Font discovery & fallback slots (previously the x.c helpers)        */
/* ------------------------------------------------------------------ */
static char *ff2_slots[FF2_MAXFONTS];
static const unsigned char *ff2_mem[FF2_MAXFONTS];
static size_t ff2_mem_len[FF2_MAXFONTS];

static const char *sysdirs[] = {
	"/usr/share/fonts",
	"/usr/local/share/fonts",
	"/usr/share/X11/fonts",
};

static char *
ff2_strdup(const char *s)
{
	size_t n = strlen(s) + 1;
	char *p = malloc(n);
	if (p) memcpy(p, s, n);
	return p;
}

/* Per-slot default font names: slot 0 (the primary) is provided by the
 * caller (config.h `font` via ff2_set_font); these are the fallback set. */
static const char *
ff2_default_slot(int slot)
{
	static const char *d[] = { NULL, "fontawesome-solid.ttf", "msyh.ttc" };
	return (slot >= 0 && slot < (int)(sizeof d / sizeof d[0])) ? d[slot] : NULL;
}

/* Resolve a configured font name to a readable file, or NULL.
 * Order: absolute path as-is; ~/.local/share/fonts/<name>; $PWD/<name>. */
static char *
ff2_discover(const char *name)
{
	const char *home;
	char path[PATH_MAX];

	if (name[0] == '/' && access(name, R_OK) == 0)
		return ff2_strdup(name);
	if (strchr(name, '/'))
		return NULL;

	if ((home = getenv("HOME")) != NULL) {
		snprintf(path, sizeof path, "%s/.local/share/fonts/%s",
				home, name);
		if (access(path, R_OK) == 0)
			return ff2_strdup(path);
	}
	if (getcwd(path, sizeof path) != NULL) {
		size_t n = strlen(path);
		snprintf(path + n, sizeof path - n, "/%s", name);
		if (access(path, R_OK) == 0)
			return ff2_strdup(path);
	}
	return NULL;
}

/* recursively search dir for a monospaced font file; stores it in out. */
static int
ff2_scan_dir(const char *dir, char *out, int depth)
{
	struct dirent *de;
	DIR *d;
	char path[PATH_MAX];

	if (depth > 3 || (d = opendir(dir)) == NULL)
		return 0;
	while ((de = readdir(d)) != NULL) {
		size_t len = strlen(de->d_name);

		if (de->d_name[0] == '.')
			continue;
		snprintf(path, sizeof path, "%s/%s", dir, de->d_name);
		if (len >= 4 &&
		    (!strcmp(de->d_name + len - 4, ".ttf") ||
		     !strcmp(de->d_name + len - 4, ".otf") ||
		     !strcmp(de->d_name + len - 4, ".TTF") ||
		     !strcmp(de->d_name + len - 4, ".OTF"))) {
			if (access(path, R_OK) == 0 && ft_fixed_width(path)) {
				closedir(d);
				snprintf(out, PATH_MAX, "%s", path);
				return 1;
			}
			continue;
		}
		/* not a font file: descend if dir (opendir fails on files) */
		if (ff2_scan_dir(path, out, depth + 1)) {
			closedir(d);
			return 1;
		}
	}
	closedir(d);

	return 0;
}

void
ff2_set_font(int slot, const char *name)
{
	if (slot < 0 || slot >= FF2_MAXFONTS)
		return;
	free(ff2_slots[slot]);
	ff2_slots[slot] = name ? ff2_strdup(name) : NULL;
}

/* Register an in-memory font for a slot (e.g. a go:embed'd TTF). The
 * caller must keep the buffer alive for the lifetime of the library;
 * ff2_load_fonts() prefers the memory font over any path for that slot. */
void
ff2_set_memory_font(int slot, const unsigned char *data, size_t len)
{
	if (slot < 0 || slot >= FF2_MAXFONTS)
		return;
	ff2_mem[slot] = data;
	ff2_mem_len[slot] = len;
}

void
ff2_unload_fonts(void)
{
	ft_cleanup();
}

int
ff2_load_fonts(int pixsize)
{
	char *plist[FF2_MAXFONTS];
	int i, j, nfont = 0;
	char *p;

	for (i = 0; i < FF2_MAXFONTS; i++) {
		if (ff2_mem[i] && ff2_mem_len[i] > 0)
			continue; /* loaded from memory in ft_init */
		const char *nm = ff2_slots[i] ? ff2_slots[i] : ff2_default_slot(i);
		if (!nm)
			continue;
		p = ff2_discover(nm);
		if (p) {
			plist[nfont++] = p;
		} else {
			fprintf(stderr, "st: font not found, trying next: %s\n", nm);
		}
	}

	int has_mem = 0;
	for (i = 0; i < FF2_MAXFONTS; i++)
		if (ff2_mem[i] && ff2_mem_len[i] > 0)
			has_mem = 1;

	if (nfont == 0 && !has_mem) {
		/* last resort: any monospaced face found on the system */
		char syspath[PATH_MAX];
		for (j = 0; j < (int)(sizeof sysdirs / sizeof sysdirs[0]); j++)
			if (ff2_scan_dir(sysdirs[j], syspath, 0)) {
				plist[nfont++] = ff2_strdup(syspath);
				break;
			}
	}
	if (nfont == 0 && !has_mem)
		return 0;

	if (!ft_init(plist, nfont, pixsize)) {
		while (nfont > 0) free(plist[--nfont]);
		return 0;
	}
	while (nfont > 0)
		free(plist[--nfont]);

	return 1;
}

int ft_init(char **path_list, int npaths, int height) {
	int i, n;

	if (FT_Init_FreeType(&ft_lib)) return 0;
	n = 0;
	/* memory fonts first (slot 0 is the go:embed'd primary unless
	 * overwritten by a path set via ff2_set_font) */
	for (i = 0; i < FF2_MAXFONTS && n < FF2_MAXFONTS; i++) {
		if (!ff2_mem[i] || ff2_mem_len[i] == 0)
			continue;
		if (FT_New_Memory_Face(ft_lib, ff2_mem[i],
		    (FT_Long)ff2_mem_len[i], 0, &fonts[n]) == 0) {
			FT_Set_Pixel_Sizes(fonts[n], 0, height);
			n++;
		}
	}
	for (i = 0; i < npaths && n < FF2_MAXFONTS; i++) {
		if (!path_list[i]) continue;
		if (FT_New_Face(ft_lib, path_list[i], 0, &fonts[n]) == 0) {
			FT_Set_Pixel_Sizes(fonts[n], 0, height);
			n++;
		}
	}
	fonts[n] = NULL;
	return 1;
}

void ft_cleanup() {
	for (int i = 0; i < FF2_MAXFONTS && fonts[i]; i++) { FT_Done_Face(fonts[i]); }
	FT_Done_FreeType(ft_lib);
}

int ft_fixed_width(const char *path) {
	FT_Library lib;
	FT_Face face;
	int mono = 0;

	if (FT_Init_FreeType(&lib))
		return 0;
	if (FT_New_Face(lib, path, 0, &face) == 0) {
		mono = FT_IS_FIXED_WIDTH(face);
		FT_Done_Face(face);
	}
	FT_Done_FreeType(lib);

	return mono;
}

int ft_metrics(int *ascent, int *descent, int *maxadvance) {
	FT_Face f;
	FT_Size_Metrics *m;

	if (!fonts[0]) return 0;
	f = fonts[0];
	m = &f->size->metrics;
	*ascent = m->ascender / 64;
	*descent = -m->descender / 64;
	*maxadvance = m->max_advance / 64;
	return 1;
}

int render_char_to_rgba(
    const char* utf8_char,
    uint32_t bg,
    uint32_t fg,
    uint32_t** out_buffer,
    int* out_width,
    int* out_height,
    int* out_baseline
) {
	int i;
	FT_Face face = NULL;
    uint32_t codepoint = utf8_to_codepoint(utf8_char);
    FT_UInt glyph_index;
    for (i = 0; fonts[i]; i++) {
        glyph_index = FT_Get_Char_Index(fonts[i], codepoint);
        if (glyph_index) {
            face = fonts[i];
            break;
        }
    }
    if (glyph_index == 0 || face == NULL || FT_Load_Glyph(face, glyph_index, FT_LOAD_DEFAULT) != 0) {
        return -1;
    }
    // Render to 8-bit grayscale bitmap
    if ( FT_Render_Glyph(face->glyph, FT_RENDER_MODE_NORMAL) != 0) {
        return -1;
    }
    FT_Bitmap* bitmap = &face->glyph->bitmap;
    // Allocate output buffer
    *out_width = bitmap->width;
    *out_height = bitmap->rows;
    *out_baseline = face->glyph->bitmap_top;
    *out_buffer = (uint32_t*)malloc(*out_width * *out_height * sizeof(uint32_t));
    if (!*out_buffer) {
        return -1;
    }
    // Render glyph with alpha blending
    for (int y = 0; y < bitmap->rows; y++) {
        for (int x = 0; x < bitmap->width; x++) {
            uint8_t alpha = bitmap->buffer[y * bitmap->pitch + x];
            uint32_t* pixel = &(*out_buffer)[y * *out_width + x];
            if (alpha > 0) {
                if (alpha == 255) {
                    *pixel = fg;
                } else {
                    // Blend colors (approximate fast version)
                    uint32_t bg_rb = bg& 0x00FF00FF;
                    uint32_t bg_g = bg& 0x0000FF00;
                    uint32_t fg_rb = fg& 0x00FF00FF;
                    uint32_t fg_g = fg& 0x0000FF00;
                    uint32_t rb = ((fg_rb - bg_rb) * alpha >> 8) + bg_rb;
                    uint32_t g = ((fg_g - bg_g) * alpha >> 8) + bg_g;
                    *pixel = (rb & 0x00FF00FF) | (g & 0x0000FF00) | (0xFF000000);
                }
            } else { *pixel = bg; }
        }
    }
    return 0;
}

int post_process_glyph(
    uint32_t* src,
    uint32_t* dst,
    int src_width,
    int src_height,
    int y_off,
    int width,  // Returns new width
    int height,       // Fixed output height
    uint32_t bg
) {
    // clip the glyph vertically to the cell
    int ypos = y_off;
    if (ypos < 0) {
        if (src_height + ypos <= 0) {
            /* glyph entirely above the cell: fill blank */
            ypos = 0;
            src_height = 0;
        } else {
            src += -ypos * src_width;
            src_height += ypos;
            ypos = 0;
        }
    }
    int x_off = (width - src_width) / 2;
    if (x_off < 0) x_off = 0;  /* glyph wider than the cell: clip left */
    int avail = width - x_off;
    int grad = src_width > avail ? avail : src_width;
    int i, y;
    /* fill the whole cell with the background first */
    for (i = 0; i < width; i++) { dst[i] = bg; }
    for (y = 1; y < height; y++) {
        memcpy(dst + width*y, dst, width*sizeof(uint32_t));
    }
    if (src_height <= 0) return width;
    for (y = 0; y < src_height && y + ypos < height; y++) {
        if (grad > 0)
            memcpy(&dst[(y + ypos) * width + x_off], &src[y * src_width], grad * sizeof(uint32_t));
    }
    return width;
}
int make_ff2_glyph(
    char* utf8_char,    // Null-terminated UTF-8 character, e.g. "A\0"
    uint32_t fg_color,    // 0xAARRGGBB
    uint32_t bg_color,    // 0xAARRGGBB
    int out_width,
    int out_height,
    int out_baseline,
    uint32_t* dst      // Pre-allocated output buffer
) {
    uint32_t* buffer;
    int width, height, baseline;
    int result = render_char_to_rgba(
        utf8_char,
        bg_color,
        fg_color,
        &buffer,
        &width,
        &height,
        &baseline
    );
    if (result != 0) return 0;
    post_process_glyph(buffer, dst, width, height, out_baseline - baseline,
                       out_width, out_height, bg_color);
    //printf("Glyph [%s] %d (%d,%d)\n", utf8_char, baseline, width, height);
    free(buffer);
    return 1;
}
