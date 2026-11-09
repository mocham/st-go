/* dummy-pdf.c - stub for poppler (pdf_bridge) when PDF support is dropped.
 *
 * Provides the same C symbols as pdf_bridge.cpp (pdf_render_page,
 * pdf_page_count) but returns failure so the Go PDF path degrades gracefully
 * (no image) instead of crashing. Compiled to dummy-pdf.o and linked in place
 * of the poppler-based bridge for the st-min/st-stb targets.
 */
#include <stddef.h>

int pdf_font_support(void) { return 0; }
void pdf_register_font(const char *name, const char *path)
{ (void)name; (void)path; }

int
pdf_render_page(const unsigned char *pdf_data, int pdf_len,
                int page, int outW, int outH,
                unsigned char *out_bgra, int fit_width)
{
	(void)pdf_data;
	(void)pdf_len;
	(void)page;
	(void)outW;
	(void)outH;
	(void)out_bgra;
	(void)fit_width;
	return 0; /* failure: no PDF support */
}

int
pdf_page_count(const unsigned char *pdf_data, int pdf_len)
{
	(void)pdf_data;
	(void)pdf_len;
	return 0; /* failure: no PDF support */
}

void *pdf_scroll_open(const unsigned char *data, int len, int max_pages)
{ (void)data; (void)len; (void)max_pages; return NULL; }
void pdf_scroll_close(void *view) { (void)view; }
int pdf_scroll_page_tops(void *view, int *tops, int capacity)
{ (void)view; (void)tops; (void)capacity; return 0; }
int pdf_scroll_search_page(void *view, int page, const char *text)
{ (void)view; (void)page; (void)text; return 0; }
int pdf_scroll_search_rects(void *view, int page, const char *text, double *rects, int capacity, int *truncated)
{ (void)view; (void)page; (void)text; (void)rects; (void)capacity; (void)truncated; return -1; }
int pdf_scroll_page_size(void *view, int page, double *width, double *height)
{ (void)view; (void)page; (void)width; (void)height; return 0; }
int pdf_scroll_text(void *view, int page, char *out, int capacity, int *truncated)
{ (void)view; (void)page; (void)out; (void)capacity; (void)truncated; return -1; }
int pdf_scroll_outline(void *view, char *out, int capacity, int *truncated)
{ (void)view; (void)out; (void)capacity; (void)truncated; return -1; }
int pdf_scroll_height(void *view, int width, int max_height)
{ (void)view; (void)width; (void)max_height; return 0; }
int pdf_scroll_render(void *view, int width, int top, int height, unsigned char *out, unsigned int background)
{ (void)view; (void)width; (void)top; (void)height; (void)out; (void)background; return 0; }
