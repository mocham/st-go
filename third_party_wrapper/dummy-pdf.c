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
                unsigned char *out_bgra)
{
	(void)pdf_data;
	(void)pdf_len;
	(void)page;
	(void)outW;
	(void)outH;
	(void)out_bgra;
	return 0; /* failure: no PDF support */
}

int
pdf_page_count(const unsigned char *pdf_data, int pdf_len)
{
	(void)pdf_data;
	(void)pdf_len;
	return 0; /* failure: no PDF support */
}
