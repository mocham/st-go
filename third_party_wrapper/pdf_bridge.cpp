// pdf_bridge.cpp implements the C bridge over poppler's C++ renderer.
// It is compiled as part of the cgo package (see pdf_cgo.go).

#include "pdf_bridge.h"

#include <poppler-document.h>
#include <poppler-page.h>
#include <poppler-page-renderer.h>

#include <vector>
#include <cstring>
#include <cstdio>
#include <cmath>
#include <memory>
#include <algorithm>

int pdf_render_page(const unsigned char *pdf_data, int pdf_len,
                    int page, int outW, int outH,
                    unsigned char *out_bgra, int fit_width)
try {
    if (!pdf_data || pdf_len <= 0 || !out_bgra || outW <= 0 || outH <= 0)
        return 0;

    poppler::byte_array data(pdf_data, pdf_data + pdf_len);
    std::unique_ptr<poppler::document> doc(poppler::document::load_from_data(&data));
    if (!doc)
        return 0;

    int pages = doc->pages();
    if (page < 0 || page >= pages) {
        return 0;
    }

    std::unique_ptr<poppler::page> pg(doc->create_page(page));
    if (!pg) {
        return 0;
    }

    // page size in points (1/72 inch)
    double pw = pg->page_rect().width();
    double ph = pg->page_rect().height();
    if (!std::isfinite(pw) || !std::isfinite(ph) || pw < 1.0 || ph < 1.0) {
        return 0;
    }

    // scale to fit outW x outH preserving aspect ratio
    double scale = fit_width ? outW / pw : outH / ph;
    double sw = pw * scale;
    if (sw > outW) {
        scale = outW / pw;
    }
    // Fractional DPI and an explicit viewport keep even enormous PDF page
    // boxes within the Go caller's already-budgeted output dimensions.
    double res = scale * 72.0;
    int renderW = (int)std::max(1.0, std::min((double)outW, std::ceil(pw * scale)));
    int renderH = (int)std::max(1.0, std::min((double)outH, std::ceil(ph * scale)));

    poppler::page_renderer r;
    r.set_render_hint(poppler::page_renderer::antialiasing, true);
    r.set_render_hint(poppler::page_renderer::text_antialiasing, true);
    poppler::image img = r.render_page(pg.get(), res, res, 0, 0, renderW, renderH);
    if (!img.is_valid()) {
        return 0;
    }

    // image is BGRA, 4 bytes/pixel (format_bgr24 with 4-byte stride)
    const char *src = img.const_data();
    int iw = img.width();
    int ih = img.height();
    int srcbpr = img.bytes_per_row();

    // blit scaled/centered into out_bgra (row-major BGRA, outW*outH*4)
    int dx0 = (outW - iw) / 2;
    int dy0 = fit_width ? 0 : (outH - ih) / 2;
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

    return 1;
} catch (...) {
    return -1; // never unwind a C++ allocation/decoder exception through cgo
}

/* Returns the number of pages in the PDF, or 0 on failure. */
int pdf_page_count(const unsigned char *pdf_data, int pdf_len)
try {
    if (!pdf_data || pdf_len <= 0)
        return 0;
    poppler::byte_array data(pdf_data, pdf_data + pdf_len);
    std::unique_ptr<poppler::document> doc(poppler::document::load_from_data(&data));
    if (!doc)
        return 0;
    int n = doc->pages();
    return n;
} catch (...) {
    return -1;
}
