#include "video_ffmpeg.h"
#include <errno.h>
#include <fcntl.h>
#include <stdatomic.h>
#include <sys/stat.h>
#include <unistd.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/imgutils.h>
#include <libswscale/swscale.h>
#include <libswresample/swresample.h>

struct st_video {
    atomic_int cancelled;
    int fd, stream[2], active, eof, flushing, rate;
    AVFormatContext *format;
    AVIOContext *io;
    AVCodecContext *codec[2];
    AVPacket *packet;
    AVFrame *frame, *picture;
    struct SwsContext *scale;
    SwrContext *resample;
    uint8_t *pcm;
    unsigned pcm_size;
    int64_t origin, next_pts[2];
};

static int interrupted(void *opaque) {
    st_video *v = opaque;
    return atomic_load(&v->cancelled);
}
static int read_file(void *opaque, uint8_t *buf, int size) {
    st_video *v = opaque;
    if (interrupted(v)) return AVERROR_EXIT;
    ssize_t n = read(v->fd, buf, size);
    return n < 0 ? AVERROR(errno) : n == 0 ? AVERROR_EOF : (int)n;
}
static int64_t seek_file(void *opaque, int64_t offset, int whence) {
    st_video *v = opaque;
    if (interrupted(v)) return AVERROR_EXIT;
    if (whence == AVSEEK_SIZE) {
        struct stat st;
        return fstat(v->fd, &st) < 0 ? AVERROR(errno) : st.st_size;
    }
    off_t result = lseek(v->fd, offset, whence & ~AVSEEK_FORCE);
    return result < 0 ? AVERROR(errno) : result;
}
st_video *st_video_alloc(void) {
    st_video *v = av_mallocz(sizeof(*v));
    if (v) {
        atomic_init(&v->cancelled, 0);
        v->fd = -1;
        v->stream[0] = v->stream[1] = v->active = -1;
    }
    return v;
}
void st_video_cancel(st_video *v) { atomic_store(&v->cancelled, 1); }
void st_video_free(st_video *v) {
    if (!v) return;
    sws_freeContext(v->scale);
    swr_free(&v->resample);
    av_free(v->pcm);
    av_packet_free(&v->packet);
    av_frame_free(&v->frame);
    av_frame_free(&v->picture);
    for (int i = 0; i < 2; i++) avcodec_free_context(&v->codec[i]);
    avformat_close_input(&v->format);
    if (v->io) av_freep(&v->io->buffer);
    avio_context_free(&v->io);
    if (v->fd >= 0) close(v->fd);
    av_free(v);
}
static int open_codec(st_video *v, int kind) {
    const AVCodec *codec = NULL;
    int index = av_find_best_stream(v->format, kind ? AVMEDIA_TYPE_AUDIO : AVMEDIA_TYPE_VIDEO,
                                   -1, -1, &codec, 0);
    if (index < 0) return index;
    AVCodecContext *c = avcodec_alloc_context3(codec);
    if (!c) return AVERROR(ENOMEM);
    v->codec[kind] = c;
    int err = avcodec_parameters_to_context(c, v->format->streams[index]->codecpar);
    if (err < 0) return err;
    c->thread_count = 2;
    c->max_pixels = 4096LL * 2160;
    c->log_level_offset = AV_LOG_MAX_OFFSET;
    if ((err = avcodec_open2(c, codec, NULL)) < 0) return err;
    v->stream[kind] = index;
    return 0;
}
int st_video_open(st_video *v, const char *path) {
    /* Open only a regular local file. Custom AVIO prevents URL protocols and
       nested file references; MOV external data references remain disabled. */
    v->fd = open(path, O_RDONLY | O_NONBLOCK | O_CLOEXEC);
    if (v->fd < 0) return AVERROR(errno);
    struct stat st;
    if (fstat(v->fd, &st) < 0) return AVERROR(errno);
    if (!S_ISREG(st.st_mode)) return AVERROR(EINVAL);
    uint8_t *buffer = av_malloc(32768);
    if (!buffer) return AVERROR(ENOMEM);
    v->io = avio_alloc_context(buffer, 32768, 0, v, read_file, NULL, seek_file);
    if (!v->io) { av_free(buffer); return AVERROR(ENOMEM); }
    v->format = avformat_alloc_context();
    if (!v->format) return AVERROR(ENOMEM);
    v->format->pb = v->io;
    v->format->flags |= AVFMT_FLAG_CUSTOM_IO;
    v->format->interrupt_callback = (AVIOInterruptCB){interrupted, v};
    v->format->probesize = 4 * 1024 * 1024;
    v->format->max_analyze_duration = 5 * AV_TIME_BASE;
    int err = avformat_open_input(&v->format, NULL, av_find_input_format("mov"), NULL);
    if (err < 0) return err;
    if ((err = avformat_find_stream_info(v->format, NULL)) < 0) return err;
    if ((err = open_codec(v, 0)) < 0) return err;
    err = open_codec(v, 1);
    if (err < 0) avcodec_free_context(&v->codec[1]);
    v->origin = v->format->start_time == AV_NOPTS_VALUE ? 0 : v->format->start_time;
    v->packet = av_packet_alloc();
    v->frame = av_frame_alloc();
    v->picture = av_frame_alloc();
    return v->packet && v->frame && v->picture ? 0 : AVERROR(ENOMEM);
}
int st_video_info(st_video *v, int *w, int *h, int *audio, int64_t *duration) {
    *w = v->codec[0]->width;
    *h = v->codec[0]->height;
    *audio = v->codec[1] != NULL;
    *duration = v->format->duration == AV_NOPTS_VALUE ? 0 : v->format->duration;
    return 0;
}
int st_video_next(st_video *v, int64_t *pts) {
    for (;;) {
        if (interrupted(v)) return AVERROR_EXIT;
        if (v->active >= 0) {
            int k = v->active;
            int err = avcodec_receive_frame(v->codec[k], v->frame);
            if (err >= 0) {
                AVStream *s = v->format->streams[v->stream[k]];
                int64_t stamp = v->frame->best_effort_timestamp;
                *pts = stamp == AV_NOPTS_VALUE ? v->next_pts[k] :
                    av_rescale_q(stamp, s->time_base, AV_TIME_BASE_Q) - v->origin;
                int64_t duration = k && v->frame->sample_rate > 0 ?
                    (int64_t)v->frame->nb_samples * AV_TIME_BASE / v->frame->sample_rate :
                    av_rescale_q(v->frame->duration, s->time_base, AV_TIME_BASE_Q);
                v->next_pts[k] = *pts + (duration > 0 ? duration : 33333);
                if (!k) {
                    av_frame_unref(v->picture);
                    if ((err = av_frame_ref(v->picture, v->frame)) < 0) return err;
                }
                return k + 1;
            }
            if (err != AVERROR(EAGAIN) && err != AVERROR_EOF) return err;
            v->active = -1;
        }
        if (v->eof) {
            while (v->flushing < 2 && !v->codec[v->flushing]) v->flushing++;
            if (v->flushing == 2) return 0;
            int k = v->flushing++;
            int err = avcodec_send_packet(v->codec[k], NULL);
            if (err < 0 && err != AVERROR_EOF) return err;
            v->active = k;
            continue;
        }
        int err = av_read_frame(v->format, v->packet);
        if (err == AVERROR_EOF) { v->eof = 1; continue; }
        if (err < 0) return err;
        for (int k = 0; k < 2; k++) {
            if (v->codec[k] && v->packet->stream_index == v->stream[k]) {
                err = avcodec_send_packet(v->codec[k], v->packet);
                if (err >= 0) v->active = k;
                break;
            }
        }
        av_packet_unref(v->packet);
        if (err < 0) return err;
    }
}
int st_video_seek(st_video *v, int64_t pts) {
    int err = avformat_seek_file(v->format, -1, INT64_MIN, pts + v->origin,
                               pts + v->origin, AVSEEK_FLAG_BACKWARD);
    if (err < 0) return err;
    for (int k = 0; k < 2; k++) {
        if (v->codec[k]) avcodec_flush_buffers(v->codec[k]);
        v->next_pts[k] = pts;
    }
    swr_free(&v->resample);
    av_frame_unref(v->picture);
    v->active = -1;
    v->eof = v->flushing = 0;
    return 0;
}
int st_video_pixels(st_video *v, uint8_t *pixels, int width, int height, int stride) {
    AVFrame *f = v->picture;
    if (!f->data[0]) return 0;
    AVRational sar = f->sample_aspect_ratio;
    double aspect = (double)f->width / f->height;
    if (sar.num > 0 && sar.den > 0) aspect *= av_q2d(sar);
    int w = width, h = (int)(width / aspect + 0.5);
    if (h > height) { h = height; w = (int)(height * aspect + 0.5); }
    w = FFMAX(1, FFMIN(w, width)); h = FFMAX(1, FFMIN(h, height));
    v->scale = sws_getCachedContext(v->scale, f->width, f->height, f->format,
                                   w, h, AV_PIX_FMT_RGB32, SWS_BILINEAR, NULL, NULL, NULL);
    if (!v->scale) return AVERROR(ENOMEM);
    uint8_t *dst[4] = {pixels + ((height-h)/2)*stride + ((width-w)/2)*4, NULL, NULL, NULL};
    int strides[4] = {stride, 0, 0, 0};
    return sws_scale(v->scale, (const uint8_t *const *)f->data, f->linesize, 0, f->height, dst, strides);
}
int st_video_audio(st_video *v, int rate, uint8_t **data, int *bytes) {
    AVFrame *f = v->frame;
    if (!v->resample || v->rate != rate) {
        swr_free(&v->resample);
        AVChannelLayout stereo = AV_CHANNEL_LAYOUT_STEREO;
        int err = swr_alloc_set_opts2(&v->resample, &stereo, AV_SAMPLE_FMT_S16, rate,
                                     &f->ch_layout, f->format, f->sample_rate, 0, NULL);
        if (err < 0) return err;
        if ((err = swr_init(v->resample)) < 0) return err;
        v->rate = rate;
    }
    int samples = swr_get_out_samples(v->resample, f->nb_samples);
    if (samples < 0) return samples;
    if (samples > 262144) return AVERROR(EINVAL);
    av_fast_malloc(&v->pcm, &v->pcm_size, (size_t)samples * 4);
    if (!v->pcm) return AVERROR(ENOMEM);
    int n = swr_convert(v->resample, &v->pcm, samples, (const uint8_t **)f->extended_data, f->nb_samples);
    if (n < 0) return n;
    *data = v->pcm; *bytes = n * 4;
    return 0;
}
