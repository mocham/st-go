/* ff2.h - public interface of the FreeType2 glyph-rendering plugin
 * (see plugin-ff2.c). Rendering goes through FreeType2 only.
 */
#ifndef FF2_H
#define FF2_H

#include <stdint.h>

/* Maximum number of fallback faces loaded into FreeType (hard limit). */
#define FF2_MAXFONTS 9

/* Load up to npaths faces from the paths array (null entries are skipped),
 * setting each to the given pixel height. Faces that cannot be opened are
 * silently skipped; the loader keeps the first successful ones, so missing
 * font files are transparently replaced by the next entry. Returns non-zero
 * only if the FreeType library initialised. Glyph lookups walk the loaded
 * faces in order, so fonts[0] wins when multiple faces carry a glyph.
 */
int ft_init(char **path_list, int npaths, int height);
void ft_cleanup(void);
int ft_metrics(int *ascent, int *descent, int *maxadvance);

/* Report whether the font file at path is fixed-width (monospaced).
 * Used for system-font discovery, before any face is loaded.
 */
int ft_fixed_width(const char *path);

/* --- Font configuration & discovery (the plugin owns the fallback set) ---
 * Up to FF2_MAXFONTS (9) fallback font slots are tried in order: a
 * character is taken from the first face that carries it, and a font whose
 * file is missing/unreadable is skipped in favour of the next slot.
 *
 * ff2_set_slot() overrides one slot (0..FF2_MAXFONTS-1) with a user font
 * name/path; slot 0 is normally reserved for the config.h `font` (call
 * ff2_set_slot(0, font) once from the caller). The remaining slots fall
 * back to per-slot defaults (e.g. an icon face and a CJK face).
 *
 * ff2_load_fonts() resolves every slot to a readable file, loads the faces
 * at the given pixel (render) size, and, when no requested font resolves,
 * falls back to any monospaced face found in the system font directories.
 * It returns non-zero on success (faces loaded into FF2).
 * ff2_unload_fonts() releases the loaded faces and the FreeType library.
 */
void ff2_set_font(int slot, const char *name);
int  ff2_load_fonts(int pixsize);
void ff2_unload_fonts(void);

/* Register an in-memory font (e.g. a go:embed'd TTF) for a slot; the buffer
 * must outlive the library. ff2_load_fonts() prefers it over any path. */
void ff2_set_memory_font(int slot, const unsigned char *data, size_t len);

/* Renders utf8_char (null-terminated UTF-8) blended against bg into dst,
 * which must be out_width*out_height uint32_t cells of 0xAARRGGBB. Returns
 * the number of output columns actually used, or 0 on failure.
 */
int make_ff2_glyph(char *utf8_char, uint32_t fg_color, uint32_t bg_color,
                   int out_width, int out_height, int out_baseline,
                   uint32_t *dst);

#endif /* FF2_H */