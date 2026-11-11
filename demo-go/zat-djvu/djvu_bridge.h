#ifndef ST_GO_ZAT_DJVU_BRIDGE_H
#define ST_GO_ZAT_DJVU_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

typedef struct zat_djvu_document zat_djvu_document;
typedef struct zat_djvu_outline zat_djvu_outline;

zat_djvu_document* zat_djvu_open(const char* path, uint32_t background);
void zat_djvu_close(zat_djvu_document* document);
const char* zat_djvu_error(const zat_djvu_document* document);
int zat_djvu_pages(const zat_djvu_document* document);

int zat_djvu_render_page(zat_djvu_document* document, int page, uint64_t max_pixels,
                         unsigned char** pixels, int* width, int* height);
void zat_djvu_free(void* pointer);

int zat_djvu_page_text(zat_djvu_document* document, int page, char* output,
                       size_t capacity, size_t* length, int* truncated);

zat_djvu_outline* zat_djvu_get_outline(zat_djvu_document* document,
                                       size_t max_entries, size_t max_bytes);
size_t zat_djvu_outline_count(const zat_djvu_outline* outline);
int zat_djvu_outline_truncated(const zat_djvu_outline* outline);
int zat_djvu_outline_page(const zat_djvu_outline* outline, size_t index);
int zat_djvu_outline_level(const zat_djvu_outline* outline, size_t index);
const char* zat_djvu_outline_title(const zat_djvu_outline* outline, size_t index);
size_t zat_djvu_outline_title_length(const zat_djvu_outline* outline, size_t index);
void zat_djvu_outline_free(zat_djvu_outline* outline);

#endif
