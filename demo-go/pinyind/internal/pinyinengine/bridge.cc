//go:build googlepinyin && cgo && linux

#include "../../../../third_party/googlepinyin-src/src/include/pinyinime.h"
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

extern "C" int gp_open(int fd, long length, int user_fd) {
    char path[64];
    snprintf(path, sizeof(path), "/proc/self/fd/%d", user_fd);
    // The decoder consumes/closes the system FD through fdopen().
    int duplicate = dup(fd);
    if (duplicate < 0) return 0;
    if (!ime_pinyin::im_open_decoder_fd(duplicate, 0, length, path)) return 0;
    ime_pinyin::im_set_max_lens(32, 32);
    return 1;
}

extern "C" void gp_close(void) { ime_pinyin::im_close_decoder(); }

extern "C" int gp_lookup(const char *prompt, unsigned length, unsigned requested_page,
                          uint16_t *out, uint32_t *lengths, uint32_t *total, uint32_t *page) {
    if (!prompt || !length || length > 32 || requested_page > 819) return -1;
    for (unsigned i = 0; i < length; ++i)
        if ((prompt[i] < 'a' || prompt[i] > 'z') && prompt[i] != '\'') return -1;
    ime_pinyin::im_reset_search();
    size_t count = ime_pinyin::im_search(prompt, length);
    if (count > 4096) count = 4096;
    *total = count;
    *page = count ? (requested_page < (count - 1) / 5 ? requested_page : (count - 1) / 5) : 0;
    unsigned first = *page * 5;
    unsigned shown = count - first < 5 ? count - first : 5;
    memset(out, 0, 5 * 128 * sizeof(uint16_t));
    for (unsigned i = 0; i < shown; ++i) {
        ime_pinyin::char16 candidate[128] = {};
        if (!ime_pinyin::im_get_candidate(first + i, candidate, 128)) return -1;
        unsigned n = 0;
        while (n < 128 && candidate[n]) { out[i * 128 + n] = candidate[n]; ++n; }
        if (n == 128) return -1;
        lengths[i] = n;
    }
    return shown;
}
