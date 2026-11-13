#ifndef ST_GO_RECORDING_FFMPEG_H
#define ST_GO_RECORDING_FFMPEG_H

#include <stdint.h>

typedef struct st_recording st_recording;

/* Each handle belongs to one recording worker. Results are AVERROR codes. */
int st_recording_open(const char *path, int width, int height, int fps,
                      int threads, st_recording **result);
int st_recording_write(st_recording *recording, const uint8_t *rgba,
                       int stride, int64_t pts);
int st_recording_finish(st_recording *recording);
void st_recording_free(st_recording *recording);

#endif
