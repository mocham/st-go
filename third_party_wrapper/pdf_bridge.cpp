// pdf_bridge.cpp implements the C bridge over poppler's C++ renderer.
// It is compiled as part of the cgo package (see pdf_cgo.go).

#include "pdf_bridge.h"

#include <poppler-document.h>
#include <poppler-page.h>
#include <poppler-page-renderer.h>
#include "GlobalParams.h"
#include "cpp/poppler-page-private.h"
#include "cpp/poppler-document-private.h"
#include "SplashOutputDev.h"
#include "Outline.h"
#include "Link.h"
#include "TextOutputDev.h"
#include "UTF.h"
#include "splash/SplashBitmap.h"

#include <vector>
#include <cstring>
#include <cstdio>
#include <cmath>
#include <memory>
#include <algorithm>
#include <set>

int pdf_font_support(void) { return 1; }

void pdf_register_font(const char *name, const char *path)
try {
    // Hold a reference for the process lifetime: standalone page decodes and
    // the continuous worker share Poppler's font map without reset races.
    static GlobalParamsIniter fonts(nullptr);
    if (name && path) globalParams->addFontFile(name, path);
} catch (...) { }

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
    // One native engine per preview document, confined to its decoder worker.
    // Reuse the font engine across slices rather than recreating it per page.
    PDFDoc *native = nullptr;
    std::unique_ptr<SplashOutputDev> renderer;
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
        if (!view->native) view->native = poppler::page_private::get(page.get())->doc->doc;
        const double w = page->page_rect().width(), h = page->page_rect().height();
        if (!std::isfinite(w) || !std::isfinite(h) || w < 1 || h < 1)
            return nullptr;
        view->pages.push_back({w, h});
    }
    SplashColor paper = {255, 255, 255};
    view->renderer = std::make_unique<SplashOutputDev>(splashModeXBGR8, 4, false, paper, true);
    view->renderer->setFontAntialias(true);
    view->renderer->setVectorAntialias(true);
    view->renderer->setFreeTypeHinting(false, false);
    view->renderer->startDoc(view->native);
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

namespace {
struct bounded_pdf_text {
    char *out;
    int capacity;
    int used = 0;
    bool truncated = false;
};

void collect_pdf_text(void *context, const char *text, int length) {
    auto &buffer = *static_cast<bounded_pdf_text *>(context);
    if (buffer.truncated || length <= 0) return;
    int count = std::min(length, buffer.capacity - buffer.used);
    // Poppler emits complete UTF-8 fragments. Trim only a budget-cut suffix;
    // once truncated, do not append later fragments past the missing text.
    while (count < length && count > 0 && ((unsigned char)text[count] & 0xc0) == 0x80) --count;
    if (count > 0) std::memcpy(buffer.out + buffer.used, text, count);
    buffer.used += count;
    buffer.truncated = count < length;
}
}

int pdf_scroll_text(void *handle, int index, char *out, int capacity, int *truncated)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || !view->native || !out || !truncated || capacity < 1 || index < 0 || index >= (int)view->pages.size()) return -1;
    // The C++ page::text()/ustring API round-trips UTF-8 through iconv's
    // dynamically loaded UTF-16 modules. On a minimal host a static binary
    // silently gets an empty string, despite rendering/counting pages normally.
    // TextOutputDev already emits UTF-8 using Poppler's built-in Unicode map.
    if (globalParams->getTextEncodingName() != "UTF-8") return -1;
    bounded_pdf_text buffer{out, capacity};
    TextOutputDev text(&collect_pdf_text, &buffer, true, 0, false);
    if (!text.isOk()) return -1;
    view->native->displayPage(&text, index + 1, 72, 72, 0, false, true, false);
    *truncated = buffer.truncated;
    return buffer.used;
} catch (...) { return -1; }

namespace {
void outline_u32(char *out, unsigned int value) {
    for (int i = 0; i < 4; ++i) out[i] = (char)(value >> (8 * i));
}
void outline_utf8(std::string &out, unsigned int c) {
    if (c > 0x10ffff || (c >= 0xd800 && c <= 0xdfff)) c = 0xfffd;
    if (c < 0x80) out += (char)c;
    else if (c < 0x800) { out += (char)(0xc0 | (c >> 6)); out += (char)(0x80 | (c & 63)); }
    else if (c < 0x10000) { out += (char)(0xe0 | (c >> 12)); out += (char)(0x80 | ((c >> 6) & 63)); out += (char)(0x80 | (c & 63)); }
    else { out += (char)(0xf0 | (c >> 18)); out += (char)(0x80 | ((c >> 12) & 63)); out += (char)(0x80 | ((c >> 6) & 63)); out += (char)(0x80 | (c & 63)); }
}
}

int pdf_scroll_outline(void *handle, char *out, int capacity, int *truncated)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || !view->native || !out || !truncated || capacity < 1) return -1;
    *truncated = 0;
    auto *outline = view->native->getOutline();
    if (!outline || !outline->getItems()) return 0;
    struct frame { const std::vector<OutlineItem *> *items; size_t index; int level; };
    std::vector<frame> stack{{outline->getItems(), 0, 0}};
    std::set<int> seen;
    int written = 0, entries = 0;
    while (!stack.empty()) {
        auto &frame = stack.back();
        if (frame.index >= frame.items->size()) { stack.pop_back(); continue; }
        auto *item = (*frame.items)[frame.index++];
        if (!item || !seen.insert(item->getRefNum()).second) continue;
        if (++entries > 4096) { *truncated = 1; break; }
        int page = 0, level = frame.level;
        const auto *action = item->getAction();
        if (action && action->getKind() == actionGoTo) {
            const auto *go = static_cast<const LinkGoTo *>(action);
            std::unique_ptr<LinkDest> named;
            const auto *dest = go->getDest();
            if (!dest && go->getNamedDest()) { named = view->native->findDest(go->getNamedDest()); dest = named.get(); }
            if (dest && dest->isOk()) page = dest->isPageRef() ? view->native->findPage(dest->getPageRef()) : dest->getPageNum();
            if (page < 1 || page > (int)view->pages.size()) page = 0;
        }
        std::string title;
        for (auto c : item->getTitle()) {
            if (title.size() >= 4096) { *truncated = 1; break; }
            outline_utf8(title, c);
        }
        if ((size_t)(capacity - written) < 12 + title.size()) { *truncated = 1; break; }
        outline_u32(out + written, page); outline_u32(out + written + 4, level); outline_u32(out + written + 8, title.size());
        std::memcpy(out + written + 12, title.data(), title.size());
        written += 12 + title.size();
        if (item->hasKids()) {
            if (level >= 31) { *truncated = 1; continue; }
            item->open();
            if (auto *kids = item->getKids()) stack.push_back({kids, 0, level + 1});
        }
    }
    return written;
} catch (...) { return -1; }

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
    SplashColor paper = {static_cast<unsigned char>(background & 255),
                         static_cast<unsigned char>((background >> 8) & 255), static_cast<unsigned char>((background >> 16) & 255)};
    view->renderer->setPaperColor(paper);
    for (; index < (int)view->pages.size() && view->tops[index] < top + height; ++index) {
        int start = std::max(top, view->tops[index]);
        int end = std::min(top + height, view->tops[index + 1]);
        double dpi = 72.0 * width / view->pages[index].width;
        view->native->displayPageSlice(view->renderer.get(), index + 1, dpi, dpi, 0, false, true, false,
                                      0, start - view->tops[index], width, end - start);
        auto *image = view->renderer->getBitmap();
        if (!image || !image->getDataPtr()) return 0;
        int rows = std::min(image->getHeight(), end - start);
        int cols = std::min(image->getWidth(), width);
        for (int y = 0; y < rows; ++y) {
            std::memcpy(out + (size_t)(start - top + y) * width * 4,
                        image->getDataPtr() + (size_t)y * image->getRowSize(), (size_t)cols * 4);
        }
    }
    return 1;
} catch (...) { return 0; }

int pdf_scroll_page_tops(void *handle, int *tops, int capacity)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || !tops || capacity < (int)view->tops.size()) return 0;
    std::copy(view->tops.begin(), view->tops.end(), tops);
    return (int)view->tops.size();
} catch (...) { return 0; }

int pdf_scroll_search_page(void *handle, int index, const char *text)
try {
    auto *view = static_cast<scroll_pdf *>(handle);
    if (!view || !text || index < 0 || index >= (int)view->pages.size()) return 0;
    const auto needle = utf8ToUCS4(text);
    if (needle.empty()) return 0;
    TextOutputDev page(nullptr, true, 0, false, false);
    view->native->displayPage(&page, index + 1, 72, 72, 0, false, true, false);
    double left = 0, top = 0, right = 0, bottom = 0;
    if (!page.findText(needle.data(), needle.size(), true, true, false, false, false, false, false, &left, &top, &right, &bottom)) return 0;
    return 1 + (int)(1000000 * std::max(0.0, std::min(1.0, top / view->pages[index].height)));
} catch (...) { return 0; }
