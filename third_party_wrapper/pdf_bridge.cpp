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

namespace {
struct scroll_page { double width, height; };
struct scroll_pdf {
    poppler::byte_array data;
    std::unique_ptr<poppler::document> doc;
    std::vector<scroll_page> pages;
    std::vector<int> tops;
    int width = 0;
};
}

void *pdf_scroll_open(const unsigned char *data, int len, int max_pages)
try {
    if (!data || len <= 0 || max_pages <= 0) return nullptr;
    std::unique_ptr<scroll_pdf> view(new scroll_pdf);
    view->data.assign(data, data + len);
    view->doc.reset(poppler::document::load_from_data(&view->data));
    if (!view->doc || view->doc->pages() < 1 || view->doc->pages() > max_pages)
        return nullptr;
    for (int i = 0; i < view->doc->pages(); ++i) {
        std::unique_ptr<poppler::page> page(view->doc->create_page(i));
        if (!page) return nullptr;
        const double w = page->page_rect().width(), h = page->page_rect().height();
        if (!std::isfinite(w) || !std::isfinite(h) || w < 1 || h < 1)
            return nullptr;
        view->pages.push_back({w, h});
    }
    return view.release();
} catch (...) { return nullptr; }

void pdf_scroll_close(void *view) { delete static_cast<scroll_pdf *>(view); }

int pdf_scroll_height(void *handle, int width, int max_height)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || width <= 0 || max_height <= 0) return 0;
    if (view->width == width && !view->tops.empty())
        return view->tops.back() <= max_height ? view->tops.back() : 0;
    std::vector<int> tops(1, 0);
    for (auto page : view->pages) {
        double h = std::ceil(page.height * ((double)width / page.width));
        if (!std::isfinite(h) || h < 1 || h > max_height - tops.back()) return 0;
        tops.push_back(tops.back() + (int)h);
    }
    view->tops.swap(tops);
    view->width = width;
    return view->tops.back();
} catch (...) { return 0; }

int pdf_scroll_render(void *handle, int width, int top, int height, unsigned char *out, unsigned int background)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || !out || view->width != width || view->tops.empty() ||
        top < 0 || top >= view->tops.back() || height <= 0 ||
        height > view->tops.back() - top) return 0;
    for (size_t i = 0; i < (size_t)width * height * 4; i += 4) {
        out[i] = background & 255;
        out[i + 1] = (background >> 8) & 255;
        out[i + 2] = (background >> 16) & 255;
        out[i + 3] = 255;
    }
    int index = (int)(std::upper_bound(view->tops.begin(), view->tops.end(), top) - view->tops.begin()) - 1;
    poppler::page_renderer renderer;
    renderer.set_paper_color(background);
    renderer.set_render_hint(poppler::page_renderer::antialiasing, true);
    renderer.set_render_hint(poppler::page_renderer::text_antialiasing, true);
    for (; index < (int)view->pages.size() && view->tops[index] < top + height; ++index) {
        int start = std::max(top, view->tops[index]);
        int end = std::min(top + height, view->tops[index + 1]);
        std::unique_ptr<poppler::page> page(view->doc->create_page(index));
        if (!page) return 0;
        double dpi = 72.0 * width / view->pages[index].width;
        auto image = renderer.render_page(page.get(), dpi, dpi, 0, start - view->tops[index], width, end - start);
        if (!image.is_valid()) return 0;
        int rows = std::min(image.height(), end - start);
        int cols = std::min(image.width(), width);
        for (int y = 0; y < rows; ++y) {
            std::memcpy(out + (size_t)(start - top + y) * width * 4,
                        image.const_data() + (size_t)y * image.bytes_per_row(), (size_t)cols * 4);
        }
    }
    return 1;
} catch (...) { return 0; }
