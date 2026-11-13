#include "recording_ffmpeg.h"

#include <errno.h>
#include <limits.h>
#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libswscale/swscale.h>

struct st_recording {
    AVFormatContext *format;
    AVCodecContext *codec;
    AVStream *stream;
    AVFrame *frame;
    AVPacket *packet;
    struct SwsContext *scale;
    int width, height;
};

void st_recording_free(st_recording *r)
{
    if (!r)
        return;
    if (r->format && r->format->pb)
        avio_closep(&r->format->pb);
    sws_freeContext(r->scale);
    av_packet_free(&r->packet);
    av_frame_free(&r->frame);
    avcodec_free_context(&r->codec);
    avformat_free_context(r->format);
    av_free(r);
}

int st_recording_open(const char *path, int width, int height, int fps,
                      int threads, st_recording **result)
{
    *result = NULL;
    if (!path || width <= 0 || height <= 0 || width >= INT_MAX / 4 ||
        height >= INT_MAX || fps <= 0 || threads <= 0)
        return AVERROR(EINVAL);
    st_recording *r = av_mallocz(sizeof(*r));
    if (!r)
        return AVERROR(ENOMEM);
    r->width = width;
    r->height = height;
    int err = avformat_alloc_output_context2(&r->format, NULL, "mp4", path);
    if (err < 0)
        goto fail;
    const AVCodec *encoder = avcodec_find_encoder_by_name("libx264");
    if (!encoder) {
        err = AVERROR_ENCODER_NOT_FOUND;
        goto fail;
    }
    r->codec = avcodec_alloc_context3(encoder);
    r->stream = avformat_new_stream(r->format, NULL);
    r->frame = av_frame_alloc();
    r->packet = av_packet_alloc();
    if (!r->codec || !r->stream || !r->frame || !r->packet) {
        err = AVERROR(ENOMEM);
        goto fail;
    }
    r->codec->width = (width + 1) & ~1;
    r->codec->height = (height + 1) & ~1;
    r->codec->pix_fmt = AV_PIX_FMT_YUV420P;
    r->codec->time_base = (AVRational){1, fps};
    r->codec->framerate = (AVRational){fps, 1};
    r->codec->gop_size = fps * 2;
    r->codec->max_b_frames = 0;
    r->codec->thread_count = threads;
    /* Do not write encoder statistics into the owned graphics console. */
    r->codec->log_level_offset = AV_LOG_MAX_OFFSET;
    if (r->format->oformat->flags & AVFMT_GLOBALHEADER)
        r->codec->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    if ((err = av_opt_set(r->codec->priv_data, "preset", "ultrafast", 0)) < 0 ||
        (err = av_opt_set(r->codec->priv_data, "tune", "zerolatency", 0)) < 0 ||
        (err = av_opt_set(r->codec->priv_data, "crf", "23", 0)) < 0 ||
        (err = avcodec_open2(r->codec, encoder, NULL)) < 0)
        goto fail;
    r->stream->time_base = r->codec->time_base;
    r->stream->avg_frame_rate = r->codec->framerate;
    if ((err = avcodec_parameters_from_context(r->stream->codecpar, r->codec)) < 0)
        goto fail;
    r->frame->format = r->codec->pix_fmt;
    r->frame->width = r->codec->width;
    r->frame->height = r->codec->height;
    if ((err = av_frame_get_buffer(r->frame, 32)) < 0)
        goto fail;
    /* Convert at the original size; the extra row/column stays black. */
    r->scale = sws_getContext(width, height, AV_PIX_FMT_RGBA,
                             width, height, AV_PIX_FMT_YUV420P,
                             SWS_BILINEAR, NULL, NULL, NULL);
    if (!r->scale) {
        err = AVERROR(ENOMEM);
        goto fail;
    }
    if ((err = avio_open(&r->format->pb, path, AVIO_FLAG_WRITE)) < 0 ||
        (err = avformat_write_header(r->format, NULL)) < 0)
        goto fail;
    *result = r;
    return 0;
fail:
    st_recording_free(r);
    return err;
}

static int write_packets(st_recording *r)
{
    for (;;) {
        int err = avcodec_receive_packet(r->codec, r->packet);
        if (err == AVERROR(EAGAIN) || err == AVERROR_EOF)
            return 0;
        if (err < 0)
            return err;
        r->packet->duration = 1;
        av_packet_rescale_ts(r->packet, r->codec->time_base, r->stream->time_base);
        r->packet->stream_index = r->stream->index;
        err = av_interleaved_write_frame(r->format, r->packet);
        av_packet_unref(r->packet);
        if (err < 0)
            return err;
    }
}

int st_recording_write(st_recording *r, const uint8_t *rgba, int stride, int64_t pts)
{
    if (!r || !rgba || stride < r->width * 4 || pts < 0)
        return AVERROR(EINVAL);
    int err = av_frame_make_writable(r->frame);
    if (err < 0)
        return err;
    for (int plane = 0; plane < 3; plane++) {
        int rows = plane ? r->frame->height / 2 : r->frame->height;
        memset(r->frame->data[plane], plane ? 128 : 16,
               (size_t)r->frame->linesize[plane] * rows);
    }
    const uint8_t *input[4] = {rgba, NULL, NULL, NULL};
    int strides[4] = {stride, 0, 0, 0};
    err = sws_scale(r->scale, input, strides, 0, r->height,
                    r->frame->data, r->frame->linesize);
    if (err < 0)
        return err;
    r->frame->pts = pts;
    r->frame->duration = 1;
    if ((err = avcodec_send_frame(r->codec, r->frame)) < 0)
        return err;
    return write_packets(r);
}

int st_recording_finish(st_recording *r)
{
    int err = avcodec_send_frame(r->codec, NULL);
    if (err >= 0)
        err = write_packets(r);
    if (err >= 0)
        err = av_write_trailer(r->format);
    /* avio_closep also reports buffered writes which fail at finalization. */
    int closed = avio_closep(&r->format->pb);
    return err < 0 ? err : closed;
}
