#include "video_ffmpeg.h"
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <stdatomic.h>
#include <stdio.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>
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
    AVFilterGraph *audio_graph;
    AVFilterContext *audio_source, *audio_sink, *tempo[2];
    AVFrame *audio_input, *audio_output;
    AVChannelLayout audio_layout;
    int audio_rate, audio_format, audio_eof;
    int64_t audio_samples;
    double audio_speed;
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
void st_video_audio_reset(st_video *v) {
    swr_free(&v->resample);
    avfilter_graph_free(&v->audio_graph);
    v->audio_source = v->audio_sink = v->tempo[0] = v->tempo[1] = NULL;
    if (v->audio_input) av_frame_unref(v->audio_input);
    if (v->audio_output) av_frame_unref(v->audio_output);
    av_channel_layout_uninit(&v->audio_layout);
    v->audio_samples = v->audio_eof = 0;
    v->audio_speed = 0;
}
void st_video_free(st_video *v) {
    if (!v) return;
    sws_freeContext(v->scale);
    st_video_audio_reset(v);
    av_free(v->pcm);
    av_packet_free(&v->packet);
    av_frame_free(&v->frame);
    av_frame_free(&v->picture);
    av_frame_free(&v->audio_input);
    av_frame_free(&v->audio_output);
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
    v->audio_input = av_frame_alloc();
    v->audio_output = av_frame_alloc();
    return v->packet && v->frame && v->picture && v->audio_input && v->audio_output ? 0 : AVERROR(ENOMEM);
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
    st_video_audio_reset(v);
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
static int configure_audio(st_video *v, int rate, double speed) {
    AVFrame *f = v->frame;
    if (!v->resample || v->rate != rate || v->audio_rate != f->sample_rate ||
        v->audio_format != f->format || av_channel_layout_compare(&v->audio_layout, &f->ch_layout)) {
        st_video_audio_reset(v);
        AVChannelLayout stereo = AV_CHANNEL_LAYOUT_STEREO;
        int err = swr_alloc_set_opts2(&v->resample, &stereo, AV_SAMPLE_FMT_S16, rate,
                                     &f->ch_layout, f->format, f->sample_rate, 0, NULL);
        if (err < 0) return err;
        if ((err = swr_init(v->resample)) < 0) return err;
        if ((err = av_channel_layout_copy(&v->audio_layout, &f->ch_layout)) < 0) return err;
        v->rate = rate;
        v->audio_rate = f->sample_rate;
        v->audio_format = f->format;
    }

    if (!v->audio_graph) {
        const AVFilter *source = avfilter_get_by_name("abuffer");
        const AVFilter *sink = avfilter_get_by_name("abuffersink");
        const AVFilter *tempo = avfilter_get_by_name("atempo");
        if (!source || !sink || !tempo) return AVERROR_FILTER_NOT_FOUND;
        v->audio_graph = avfilter_graph_alloc();
        if (!v->audio_graph) return AVERROR(ENOMEM);
        v->audio_graph->nb_threads = 1;
        char args[128];
        snprintf(args, sizeof(args), "time_base=1/%d:sample_rate=%d:sample_fmt=s16:channel_layout=stereo", rate, rate);
        int err = avfilter_graph_create_filter(&v->audio_source, source, "input", args, NULL, v->audio_graph);
        if (err < 0) return err;
        if ((err = avfilter_graph_create_filter(&v->tempo[0], tempo, "tempo0", NULL, NULL, v->audio_graph)) < 0 ||
            (err = avfilter_graph_create_filter(&v->tempo[1], tempo, "tempo1", NULL, NULL, v->audio_graph)) < 0 ||
            (err = avfilter_graph_create_filter(&v->audio_sink, sink, "output", NULL, NULL, v->audio_graph)) < 0 ||
            (err = avfilter_link(v->audio_source, 0, v->tempo[0], 0)) < 0 ||
            (err = avfilter_link(v->tempo[0], 0, v->tempo[1], 0)) < 0 ||
            (err = avfilter_link(v->tempo[1], 0, v->audio_sink, 0)) < 0 ||
            (err = avfilter_graph_config(v->audio_graph, NULL)) < 0)
            return err;
    }
    if (v->audio_speed != speed) {
        /* Two WSOLA stages cover 0.25x..4x while keeping each stage within
           0.5x..2x. Runtime commands retain overlap/history across rate changes. */
        char factor[32];
        snprintf(factor, sizeof(factor), "%.17g", sqrt(speed));
        for (int i = 0; i < 2; i++) {
            int err = avfilter_process_command(v->tempo[i], "tempo", factor, NULL, 0, 0);
            if (err < 0) return err;
        }
        v->audio_speed = speed;
    }
    return 0;
}

/* One bounded PCM batch per decoded frame/EOF, not an accumulating queue. */
static int collect_audio(st_video *v, int *bytes) {
    for (;;) {
        if (interrupted(v)) return AVERROR_EXIT;
        int err = av_buffersink_get_frame(v->audio_sink, v->audio_output);
        if (err == AVERROR(EAGAIN) || err == AVERROR_EOF) return 0;
        if (err < 0) return err;
        AVFrame *f = v->audio_output;
        if (f->format != AV_SAMPLE_FMT_S16 || f->ch_layout.nb_channels != 2 ||
            f->sample_rate != v->rate || f->nb_samples < 0 || f->nb_samples > 262144 - *bytes / 4) {
            av_frame_unref(f);
            return AVERROR(EINVAL);
        }
        int size = f->nb_samples * 4;
        if (size) {
            void *buffer = av_fast_realloc(v->pcm, &v->pcm_size, *bytes + size);
            if (!buffer) { av_frame_unref(f); return AVERROR(ENOMEM); }
            v->pcm = buffer;
            memcpy(v->pcm + *bytes, f->data[0], size);
            *bytes += size;
        }
        av_frame_unref(f);
    }
}

static int feed_audio(st_video *v, const AVFrame *decoded, int *bytes) {
    int samples = swr_get_out_samples(v->resample, decoded ? decoded->nb_samples : 0);
    if (samples < 0) return samples;
    if (samples > 262144) return AVERROR(EINVAL);
    if (!samples) return 0;
    AVFrame *f = v->audio_input;
    av_frame_unref(f);
    f->format = AV_SAMPLE_FMT_S16;
    f->sample_rate = v->rate;
    av_channel_layout_default(&f->ch_layout, 2);
    f->nb_samples = samples;
    int err = av_frame_get_buffer(f, 0);
    if (err < 0) return err;
    int n = swr_convert(v->resample, f->data, samples,
                        decoded ? (const uint8_t **)decoded->extended_data : NULL,
                        decoded ? decoded->nb_samples : 0);
    if (n <= 0) return n;
    f->nb_samples = n;
    f->pts = v->audio_samples;
    f->duration = n;
    v->audio_samples += n;
    if ((err = av_buffersrc_add_frame(v->audio_source, f)) < 0) return err;
    if ((err = collect_audio(v, bytes)) < 0) return err;
    return n;
}

int st_video_audio(st_video *v, int rate, double speed, int flush, uint8_t **data, int *bytes) {
    *data = NULL; *bytes = 0;
    if (interrupted(v)) return AVERROR_EXIT;
    if (rate <= 0 || rate > 384000 || !isfinite(speed) || speed < 0.25 || speed > 4)
        return AVERROR(EINVAL);
    if (flush) {
        if (!v->audio_graph || v->audio_eof) return 0;
        int err;
        while ((err = feed_audio(v, NULL, bytes)) > 0) {}
        if (err < 0) return err;
        if ((err = av_buffersrc_close(v->audio_source, v->audio_samples, 0)) < 0) return err;
        v->audio_eof = 1;
        if ((err = collect_audio(v, bytes)) < 0) return err;
    } else {
        if (v->audio_eof) return AVERROR_EOF;
        int err = configure_audio(v, rate, speed);
        if (err < 0) return err;
        if ((err = feed_audio(v, v->frame, bytes)) < 0) return err;
    }
    *data = v->pcm;
    return 0;
}
