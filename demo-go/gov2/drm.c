//go:build linux && cgo && ffmpeg_static && !purego

#include "gov2_drm.h"
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>
#include <xf86drm.h>
#include <xf86drmMode.h>
#include <drm_fourcc.h>

struct buffer {
    uint32_t handle, fb, pitch;
    uint64_t size;
    void *map;
};
struct gov_drm {
    int fd, active, changed, front, pending, queued;
    drmModeCrtc *saved;
    uint32_t *connectors;
    int connector_count;
    struct buffer buffers[2];
};

static int failure(void) { return -(errno ? errno : EIO); }

/* Preserve the active mode and every cloned connector on its CRTC. Changing
   resolution would invalidate the framebuffer mapping owned by the parent. */
static int select_output(gov_drm *d, uint32_t requested) {
    drmModeRes *r = drmModeGetResources(d->fd);
    if (!r) return failure();
    int error = -ENODEV;
    for (int i = 0; i < r->count_connectors && !d->saved; i++) {
        drmModeConnector *c = drmModeGetConnector(d->fd, r->connectors[i]);
        if (!c) continue;
        if ((!requested || requested == c->connector_id) && c->connection == DRM_MODE_CONNECTED && c->encoder_id) {
            drmModeEncoder *e = drmModeGetEncoder(d->fd, c->encoder_id);
            if (e) {
                drmModeCrtc *crtc = e->crtc_id ? drmModeGetCrtc(d->fd, e->crtc_id) : NULL;
                if (crtc && crtc->mode_valid && crtc->buffer_id && crtc->mode.hdisplay >= 160 &&
                    crtc->mode.vdisplay >= 100 && (uint64_t)crtc->mode.hdisplay * crtc->mode.vdisplay <= 4096ULL * 2160) {
                    d->saved = crtc;
                } else if (crtc) drmModeFreeCrtc(crtc);
                drmModeFreeEncoder(e);
            }
        }
        drmModeFreeConnector(c);
    }
    if (d->saved) {
        d->connectors = calloc(r->count_connectors, sizeof(uint32_t));
        if (!d->connectors) { error = -ENOMEM; goto out; }
        for (int i = 0; i < r->count_connectors; i++) {
            drmModeConnector *c = drmModeGetConnector(d->fd, r->connectors[i]);
            if (!c) { error = failure(); goto out; }
            if (c->encoder_id) {
                drmModeEncoder *e = drmModeGetEncoder(d->fd, c->encoder_id);
                if (!e) { drmModeFreeConnector(c); error = failure(); goto out; }
                if (e->crtc_id == d->saved->crtc_id) d->connectors[d->connector_count++] = c->connector_id;
                drmModeFreeEncoder(e);
            }
            drmModeFreeConnector(c);
        }
        error = d->connector_count ? 0 : -ENODEV;
    }
out:
    drmModeFreeResources(r);
    return error;
}

static int create_buffer(gov_drm *d, struct buffer *b) {
    /* Extra row supplies padding for vectorized swscale stores at the end. */
    struct drm_mode_create_dumb create = { .width = d->saved->mode.hdisplay,
        .height = d->saved->mode.vdisplay + 1, .bpp = 32 };
    if (drmIoctl(d->fd, DRM_IOCTL_MODE_CREATE_DUMB, &create)) return failure();
    b->handle = create.handle; b->pitch = create.pitch; b->size = create.size;
    if (b->pitch % 4 || b->pitch < create.width * 4 || b->size < (uint64_t)b->pitch * create.height ||
        b->size > 128ULL * 1024 * 1024) return -EINVAL;
    uint32_t handles[4] = {b->handle}, pitches[4] = {b->pitch}, offsets[4] = {0};
    if (drmModeAddFB2(d->fd, create.width, create.height - 1, DRM_FORMAT_XRGB8888,
                      handles, pitches, offsets, &b->fb, 0)) return failure();
    struct drm_mode_map_dumb map = { .handle = b->handle };
    if (drmIoctl(d->fd, DRM_IOCTL_MODE_MAP_DUMB, &map)) return failure();
    b->map = mmap(NULL, b->size, PROT_READ | PROT_WRITE, MAP_SHARED, d->fd, map.offset);
    if (b->map == MAP_FAILED) { b->map = NULL; return failure(); }
    memset(b->map, 0, b->size);
    return 0;
}

static void flipped(int fd, unsigned frame, unsigned sec, unsigned usec, void *opaque) {
    (void)fd; (void)frame; (void)sec; (void)usec;
    gov_drm *d = opaque;
    d->front = d->queued;
    d->pending = 0;
}
static int64_t milliseconds(void) {
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return (int64_t)now.tv_sec * 1000 + now.tv_nsec / 1000000;
}
static int wait_flip(gov_drm *d) {
    int64_t end = milliseconds() + 250;
    drmEventContext context = { .version = 2, .page_flip_handler = flipped };
    while (d->pending) {
        int remaining = (int)(end - milliseconds());
        if (remaining <= 0) return -ETIMEDOUT;
        struct pollfd p = { .fd = d->fd, .events = POLLIN };
        int n = poll(&p, 1, remaining);
        if (n < 0) { if (errno == EINTR) continue; return failure(); }
        if (p.revents & (POLLERR | POLLHUP | POLLNVAL)) return -EIO;
        if ((p.revents & POLLIN) && drmHandleEvent(d->fd, &context)) return failure();
    }
    return 0;
}

gov_drm *gov_drm_open(const char *path, uint32_t connector, int *error) {
#if __BYTE_ORDER__ != __ORDER_LITTLE_ENDIAN__
    *error = -ENOTSUP; return NULL;
#endif
    gov_drm *d = calloc(1, sizeof(*d));
    if (!d) { *error = -ENOMEM; return NULL; }
    d->fd = open(path, O_RDWR | O_CLOEXEC | O_NONBLOCK);
    if (d->fd < 0) { *error = failure(); goto bad; }
    uint64_t dumb = 0;
    if (drmGetCap(d->fd, DRM_CAP_DUMB_BUFFER, &dumb) || !dumb) { *error = -ENOTSUP; goto bad; }
    if (drmSetMaster(d->fd)) { *error = failure(); goto bad; }
    d->active = 1;
    if ((*error = select_output(d, connector)) < 0) goto bad;
    for (int i = 0; i < 2; i++) if ((*error = create_buffer(d, &d->buffers[i])) < 0) goto bad;
    if ((*error = gov_drm_resume(d)) < 0) goto bad;
    return d;
bad:
    gov_drm_close(d);
    return NULL;
}

uint32_t *gov_drm_back(gov_drm *d, int *width, int *height, int *stride, int *error) {
    /* Retire the previous flip before reusing its old front buffer. Waiting
       here lets decode/audio proceed while scanout completes asynchronously. */
    if ((*error = wait_flip(d)) < 0) return NULL;
    *width = d->saved->mode.hdisplay; *height = d->saved->mode.vdisplay;
    struct buffer *b = &d->buffers[1-d->front];
    *stride = b->pitch / 4;
    return b->map;
}
int gov_drm_flip(gov_drm *d) {
    if (!d->active || d->pending) return -EBUSY;
    d->queued = 1-d->front;
    if (drmModePageFlip(d->fd, d->saved->crtc_id, d->buffers[d->queued].fb, DRM_MODE_PAGE_FLIP_EVENT, d)) return failure();
    d->pending = 1;
    return 0;
}
static int restore(gov_drm *d) {
    if (!d->active || !d->changed) return 0;
    /* Also attempt modeset restoration after a timed-out flip. Keep the fd and
       event cookie alive until cleanup completes; never reuse an unretired BO. */
    (void)wait_flip(d);
    if (drmModeSetCrtc(d->fd, d->saved->crtc_id, d->saved->buffer_id,
                       d->saved->x, d->saved->y, d->connectors, d->connector_count, &d->saved->mode)) return failure();
    d->changed = 0;
    return 0;
}
int gov_drm_suspend(gov_drm *d) {
    int error = wait_flip(d);
    if (error) return error;
    error = restore(d);
    if (error) return error;
    if (d->active && drmDropMaster(d->fd)) return failure();
    d->active = 0;
    return 0;
}
int gov_drm_resume(gov_drm *d) {
    if (!d->active && drmSetMaster(d->fd)) return failure();
    d->active = 1;
    if (drmModeSetCrtc(d->fd, d->saved->crtc_id, d->buffers[d->front].fb, 0, 0,
                       d->connectors, d->connector_count, &d->saved->mode)) return failure();
    d->changed = 1;
    return 0;
}
int gov_drm_close(gov_drm *d) {
    if (!d) return 0;
    int error = restore(d);
    for (int i = 0; i < 2; i++) {
        struct buffer *b = &d->buffers[i];
        if (b->fb) drmModeRmFB(d->fd, b->fb);
        if (b->map) munmap(b->map, b->size);
        if (b->handle) {
            struct drm_mode_destroy_dumb destroy = { .handle = b->handle };
            drmIoctl(d->fd, DRM_IOCTL_MODE_DESTROY_DUMB, &destroy);
        }
    }
    if (d->active) drmDropMaster(d->fd);
    if (d->fd >= 0) close(d->fd);
    if (d->saved) drmModeFreeCrtc(d->saved);
    free(d->connectors);
    free(d);
    return error;
}
