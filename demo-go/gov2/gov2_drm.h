#ifndef GOV2_DRM_H
#define GOV2_DRM_H
#include <stdint.h>
typedef struct gov_drm gov_drm;
gov_drm *gov_drm_open(const char *path, uint32_t connector, int *error);
uint32_t *gov_drm_back(gov_drm *d, int *width, int *height, int *stride, int *error);
int gov_drm_flip(gov_drm *d);
int gov_drm_suspend(gov_drm *d);
int gov_drm_resume(gov_drm *d);
int gov_drm_close(gov_drm *d);
#endif
