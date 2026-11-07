/* dummy-pdf.c - stub for poppler (pdf_bridge) when PDF support is dropped.
 *
 * Provides the same C symbols as pdf_bridge.cpp (pdf_render_page,
 * pdf_page_count) but returns failure so the Go PDF path degrades gracefully
 * (no image) instead of crashing. Compiled to dummy-pdf.o and linked in place
 * of the poppler-based bridge for the st-min/st-stb targets.
 */
#include <stddef.h>

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
int pdf_scroll_height(void *view, int width, int max_height)
{ (void)view; (void)width; (void)max_height; return 0; }
int pdf_scroll_render(void *view, int width, int top, int height, unsigned char *out)
{ (void)view; (void)width; (void)top; (void)height; (void)out; return 0; }
