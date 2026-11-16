/* Exercise the actual C bridge's buffer retirement and VT transitions with
   deterministic DRM events. No device is opened or modeset by this test. */
/* Rename the mocked calls instead of interposing library symbols: the bridge
   and this test link the same static libdrm archive. */
#define drmModePageFlip test_page_flip
#define drmModeSetCrtc test_set_crtc
#define drmHandleEvent test_handle_event
#define drmDropMaster test_drop_master
#define drmSetMaster test_set_master
#define poll test_poll
#include "../drm.c"
#include <assert.h>

static gov_drm *cookie;
static int submitted, retired, restored, dropped, mastered, poll_error, flip_error;

int drmModePageFlip(int fd, uint32_t crtc, uint32_t fb, uint32_t flags, void *data) {
    assert(fd == 42 && crtc == 7 && (fb == 100 || fb == 101));
    assert(flags == DRM_MODE_PAGE_FLIP_EVENT);
    if (flip_error) { errno = EIO; return -1; }
    assert(!cookie);
    cookie = data;
    submitted++;
    return 0;
}
int poll(struct pollfd *fds, nfds_t count, int timeout) {
    assert(count == 1 && timeout > 0 && timeout <= 250);
    if (poll_error) { errno = EIO; return -1; }
    fds[0].revents = POLLIN;
    return 1;
}
int drmHandleEvent(int fd, drmEventContextPtr context) {
    assert(fd == 42 && cookie && cookie->pending);
    gov_drm *d = cookie;
    cookie = NULL;
    context->page_flip_handler(fd, 1, 0, 0, d);
    retired++;
    return 0;
}
int drmModeSetCrtc(int fd, uint32_t crtc, uint32_t fb, uint32_t x, uint32_t y,
                   uint32_t *connectors, int count, drmModeModeInfoPtr mode) {
    (void)x; (void)y;
    assert(fd == 42 && crtc == 7 && mode->hdisplay == 320 && mode->vdisplay == 180);
    assert(count == 2 && connectors[0] == 8 && connectors[1] == 9);
    assert(!cookie); /* Never restore/reuse scanout ahead of flip retirement. */
    if (fb == 99) restored++;
    else assert(fb == 100 || fb == 101);
    return 0;
}
int drmDropMaster(int fd) { assert(fd == 42); dropped++; return 0; }
int drmSetMaster(int fd) { assert(fd == 42); mastered++; return 0; }

static void paint(uint32_t *pixels, uint32_t frame) {
    for (int y = 0; y < 180; y++)
        for (int x = 0; x < 320; x++)
            pixels[y * 320 + x] = frame + y * 320 + x;
}
static void check_scanout(uint32_t *pixels, int stride, uint32_t frame) {
    for (int y = 0; y < 181; y++) {
        for (int x = 0; x < stride; x++) {
            uint32_t expected = frame && y < 180 && x < 320 ? frame + y * 320 + x : 0xdeadbeef;
            assert(pixels[y * stride + x] == expected);
        }
    }
}

int main(void) {
    uint32_t front[336*181], back[352*181], shadow[320*181], connectors[] = {8, 9};
    for (unsigned i = 0; i < sizeof(front)/sizeof(*front); i++) front[i] = 0xdeadbeef;
    for (unsigned i = 0; i < sizeof(back)/sizeof(*back); i++) back[i] = 0xdeadbeef;
    drmModeCrtc saved = { .crtc_id = 7, .buffer_id = 99,
        .mode = { .hdisplay = 320, .vdisplay = 180 } };
    gov_drm d = { .fd = 42, .active = 1, .changed = 1, .saved = &saved,
        .connectors = connectors, .connector_count = 2,
        .shadow = shadow, .shadow_stride = 320,
        .buffers = {{ .map = front, .fb = 100, .pitch = 336*4 },
                    { .map = back, .fb = 101, .pitch = 352*4 }} };
    int w, h, stride, error;
    assert(gov_drm_back(&d, &w, &h, &stride, &error) == shadow);
    assert(!error && w == 320 && h == 180 && stride == 320);
    paint(shadow, 0xff100000);
    check_scanout(front, 336, 0);
    check_scanout(back, 352, 0);
    assert(gov_drm_flip(&d) == 0);
    assert(submitted == 1 && !retired && d.pending && d.front == 0);
    check_scanout(front, 336, 0);
    check_scanout(back, 352, 0xff100000);
    /* CPU rendering overlaps the outstanding flip without touching scanout. */
    assert(gov_drm_back(&d, &w, &h, &stride, &error) == shadow);
    assert(!error && !retired && d.pending && d.front == 0);
    paint(shadow, 0xff200000);
    check_scanout(back, 352, 0xff100000);
    assert(gov_drm_flip(&d) == 0);
    assert(retired == 1 && d.pending && d.front == 1);
    check_scanout(front, 336, 0xff200000);
    check_scanout(back, 352, 0xff100000);
    assert(gov_drm_suspend(&d) == 0);
    assert(retired == 2 && restored == 1 && dropped == 1 && !d.active);
    assert(gov_drm_flip(&d) == -EBUSY);
    assert(!gov_drm_back(&d, &w, &h, &stride, &error) && error == -EACCES);
    assert(gov_drm_resume(&d) == 0);
    assert(mastered == 1 && d.active && d.changed);
    paint(shadow, 0xff300000);
    flip_error = 1;
    assert(gov_drm_flip(&d) == -EIO && !d.pending);
    check_scanout(front, 336, 0xff200000);
    flip_error = 0;
    assert(gov_drm_flip(&d) == 0);
    assert(gov_drm_back(&d, &w, &h, &stride, &error) == shadow && !error);
    paint(shadow, 0xff400000);
    poll_error = 1;
    assert(gov_drm_flip(&d) == -EIO);
    assert(d.pending && d.front == 0);
    /* An event failure must prevent upload into either scanout buffer. */
    check_scanout(front, 336, 0xff200000);
    check_scanout(back, 352, 0xff300000);
    assert(gov_drm_suspend(&d) == -EIO && d.active && dropped == 1);
    poll_error = 0;
    assert(gov_drm_suspend(&d) == 0);
    assert(retired == 3 && restored == 2 && dropped == 2);
    return 0;
}
