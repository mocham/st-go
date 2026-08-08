// pdf_bridge.cpp implements the C bridge over poppler's C++ renderer.
// It is compiled as part of the cgo package (see pdf_cgo.go).

#include "pdf_bridge.h"

#include <poppler-document.h>
#include <poppler-page.h>
#include <poppler-page-renderer.h>

#include <vector>
#include <cstring>
#include <cstdio>

int pdf_render_page(const unsigned char *pdf_data, int pdf_len,
                    int page, int outW, int outH,
                    unsigned char *out_bgra)
{
    if (!pdf_data || pdf_len <= 0 || !out_bgra || outW <= 0 || outH <= 0)
        return 0;

    poppler::byte_array data(pdf_data, pdf_data + pdf_len);
    poppler::document *doc = poppler::document::load_from_data(&data);
    if (!doc)
        return 0;

    int pages = doc->pages();
    if (page < 0 || page >= pages) {
        delete doc;
        return 0;
    }

    poppler::page *pg = doc->create_page(page);
    if (!pg) {
        delete doc;
        return 0;
    }

    // page size in points (1/72 inch)
    double pw = pg->page_rect().width();
    double ph = pg->page_rect().height();
    if (pw < 1.0 || ph < 1.0) {
        delete pg;
        delete doc;
        return 0;
    }

    // scale to fit outW x outH preserving aspect ratio
    double scale = outH / ph;
    double sw = pw * scale;
    if (sw > outW) {
        scale = outW / pw;
    }
    int res = (int)(scale * 72.0);
    if (res < 1)
        res = 1;

    poppler::page_renderer r;
    r.set_render_hint(poppler::page_renderer::antialiasing, true);
    r.set_render_hint(poppler::page_renderer::text_antialiasing, true);
    poppler::image img = r.render_page(pg, res, res);
    if (!img.is_valid()) {
        delete pg;
        delete doc;
        return 0;
    }

    // image is BGRA, 4 bytes/pixel (format_bgr24 with 4-byte stride)
    const char *src = img.const_data();
    int iw = img.width();
    int ih = img.height();
    int srcbpr = img.bytes_per_row();

    // blit scaled/centered into out_bgra (row-major BGRA, outW*outH*4)
    int dx0 = (outW - iw) / 2;
    int dy0 = (outH - ih) / 2;
    if (dx0 < 0)
        dx0 = 0;
    if (dy0 < 0)
        dy0 = 0;
    int cw = iw, ch = ih;
    if (cw > outW)
        cw = outW;
    if (ch > outH)
        ch = outH;
    for (int y = 0; y < ch; ++y) {
        const unsigned char *srow = (const unsigned char *)src + (size_t)y * srcbpr;
        unsigned char *drow = out_bgra + (size_t)(y + dy0) * outW * 4 + (size_t)dx0 * 4;
        std::memcpy(drow, srow, (size_t)cw * 4);
    }

    delete pg;
    delete doc;
    return 1;
}
