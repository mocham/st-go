#ifndef PDF_BRIDGE_H
#define PDF_BRIDGE_H

#ifdef __cplusplus
extern "C" {
#endif

/* Renders page `page` (0-based) of a PDF given as raw bytes into a BGRA
 * buffer scaled to fit outW x outH. Returns 1 on success, 0 on failure. */
int pdf_render_page(const unsigned char *pdf_data, int pdf_len,
                    int page, int outW, int outH,
                    unsigned char *out_bgra);

#ifdef __cplusplus
}
#endif

#endif
