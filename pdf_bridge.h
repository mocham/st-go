#ifndef PDF_BRIDGE_H
#define PDF_BRIDGE_H

#ifdef __cplusplus
extern "C" {
#endif

/* Renders page `page` (0-based) of a PDF given as raw bytes into a BGRA
 * buffer scaled to fit outW x outH. Returns 1 on success, 0 on failure,
 * or -1 on a caught native exception. fit_width scales to outW and clips
 * vertically rather than shrinking to fit the full page. */
int pdf_render_page(const unsigned char *pdf_data, int pdf_len,
                    int page, int outW, int outH,
                    unsigned char *out_bgra, int fit_width);

/* Returns the number of pages in the PDF, or 0 on failure. */
int pdf_page_count(const unsigned char *pdf_data, int pdf_len);

/* A continuous PDF view is owned and used by one decoder worker. Rendering
 * crops to a bounded strip; it never allocates a document-sized bitmap. */
void *pdf_scroll_open(const unsigned char *data, int len, int max_pages);
void pdf_scroll_close(void *view);
int pdf_scroll_height(void *view, int width, int max_height);
int pdf_scroll_render(void *view, int width, int top, int height,
                      unsigned char *out_bgra);

#ifdef __cplusplus
}
#endif

#endif
