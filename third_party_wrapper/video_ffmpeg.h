#ifndef ST_GO_VIDEO_FFMPEG_H
#define ST_GO_VIDEO_FFMPEG_H
#include <stdint.h>
typedef struct st_video st_video;
st_video *st_video_alloc(void);
void st_video_cancel(st_video *v);
void st_video_free(st_video *v);
int st_video_open(st_video *v, const char *path);
int st_video_info(st_video *v, int *width, int *height, int *audio, int64_t *duration);
/* next: 1 video, 2 audio, 0 EOF, negative AVERROR. Time is microseconds. */
int st_video_next(st_video *v, int64_t *pts);
int st_video_seek(st_video *v, int64_t pts);
int st_video_pixels(st_video *v, uint8_t *pixels, int width, int height, int stride);
int st_video_audio(st_video *v, int rate, uint8_t **data, int *bytes);
#endif
