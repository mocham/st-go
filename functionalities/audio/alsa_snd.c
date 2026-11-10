//go:build !alsa_static

/*
 * alsa_snd.c — minimal ALSA playback wrapper owned by the terminal audio
 * service. Adapted from gowinmgr's CPlugins/plugin-snd.c:
 *
 *   - Opens the hardware device directly (hw:card,0) rather than the "default"
 *     PCM. "default" routes through dmix, whose config parse calls
 *     getgrnam_r (patched out for static linking), which breaks when ipc_gid
 *     is a group name. The direct hw device sidesteps that path entirely.
 *   - Installs an ALSA error handler (snd_lib_error_set_handler) that appends
 *     every SNDERR message to the file named by $ST_GO_ALSA_LOG instead of
 *     writing to stderr, so ALSA diagnostics never corrupt the terminal UI.
 *
 * Ordinary Go builds compile this wrapper with system ALSA. The Makefile's
 * alsa_static profile compiles the identical source against its static library.
 */
#include <alsa/asoundlib.h>
#include <alsa/error.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>

typedef struct alsa_player {
	snd_pcm_t *pcm;
	int channels;
} alsa_player_t;

/* --- error logging -------------------------------------------------------- */

static void alsa_error_handler(const char *file, int line,
			       const char *function, int err,
			       const char *fmt, ...) {
	const char *path = getenv("ST_GO_ALSA_LOG");
	FILE *f = (path && *path) ? fopen(path, "a") : NULL;
	if (f == NULL)
		return; /* no log configured: drop the message */
	va_list ap;
	va_start(ap, fmt);
	if (file && *file)
		fprintf(f, "%s:%d", file, line);
	if (function && *function)
		fprintf(f, " %s", function);
	if (err)
		fprintf(f, " (%d)", err);
	fprintf(f, ": ");
	vfprintf(f, fmt, ap);
	fputc('\n', f);
	fclose(f);
	va_end(ap);
}

void alsa_init_log(void) {
	snd_lib_error_set_handler(alsa_error_handler);
}

/* test hook: routes a message through the ALSA error handler */
void alsa_emit_error(const char *msg) {
	SNDERR("%s", msg);
}

/* --- playback ------------------------------------------------------------- */

/* Returns the first sound card index, or -1. */
static int first_card(void) {
	int card = -1;
	if (snd_card_next(&card) < 0)
		return -1;
	return card;
}

/* Configures a playback stream on an already-opened pcm. Returns the player
 * handle or NULL (which closes pcm). */
static alsa_player_t *configure_pcm(snd_pcm_t *pcm, unsigned int sample_rate,
				    int channels) {
	snd_pcm_hw_params_t *params;
	snd_pcm_hw_params_alloca(&params);
	int err = snd_pcm_hw_params_any(pcm, params);
	if (err < 0)
		goto fail;
	if ((err = snd_pcm_hw_params_set_access(pcm, params, SND_PCM_ACCESS_RW_INTERLEAVED)) < 0)
		goto fail;
	if ((err = snd_pcm_hw_params_set_format(pcm, params, SND_PCM_FORMAT_S16_LE)) < 0)
		goto fail;
	if ((err = snd_pcm_hw_params_set_channels(pcm, params, channels)) < 0)
		goto fail;
	unsigned int rate = sample_rate;
	if ((err = snd_pcm_hw_params_set_rate_near(pcm, params, &rate, 0)) < 0)
		goto fail;
	if ((err = snd_pcm_hw_params(pcm, params)) < 0)
		goto fail;

	alsa_player_t *player = malloc(sizeof(alsa_player_t));
	if (player == NULL)
		goto fail;
	player->pcm = pcm;
	player->channels = channels;
	return player;

fail:
	snd_pcm_close(pcm);
	return NULL;
}

void *alsa_player_open(unsigned int sample_rate, int channels) {
	int card = first_card();
	while (card >= 0) {
		char name[32];
		snprintf(name, sizeof(name), "hw:%d,0", card);
		snd_pcm_t *pcm = NULL;
		if (snd_pcm_open(&pcm, name, SND_PCM_STREAM_PLAYBACK, 0) == 0) {
			alsa_player_t *player = configure_pcm(pcm, sample_rate, channels);
			if (player)
				return player;
		}
		if (snd_card_next(&card) < 0)
			break;
	}
	return NULL;
}

int alsa_player_send(void *handle, const void *data, size_t bytes) {
	alsa_player_t *player = (alsa_player_t *)handle;
	size_t frames = bytes / (2 * (size_t)player->channels); /* 16-bit samples */
	snd_pcm_sframes_t written = snd_pcm_writei(player->pcm, data, frames);
	if (written < 0) {
		if (written == -EPIPE) {
			/* underrun: reset and continue */
			snd_pcm_prepare(player->pcm);
			return 0;
		}
		return -1;
	}
	return (int)(written * 2 * player->channels);
}

void alsa_player_close(void *handle) {
	alsa_player_t *player = (alsa_player_t *)handle;
	if (player) {
		if (player->pcm) {
			snd_pcm_drain(player->pcm);
			snd_pcm_close(player->pcm);
		}
		free(player);
	}
}

/* --- output-selector inspection and switching ---------------------------- */

int alsa_output_label(const char *label) {
	char lower[128];
	size_t i = 0;
	for (; label[i] && i + 1 < sizeof(lower); ++i)
		lower[i] = (char)tolower((unsigned char)label[i]);
	lower[i] = '\0';
	int speaker = strstr(lower, "speaker") != NULL || strcmp(lower, "spk") == 0;
	int earphone = strstr(lower, "earphone") != NULL ||
		strstr(lower, "headphone") != NULL || strstr(lower, "headset") != NULL ||
		strcmp(lower, "hp") == 0;
	return speaker == earphone ? -1 : earphone;
}

static int card_output_mode(int card, const char *requested, int desired) {
	char device[32];
	snprintf(device, sizeof(device), "hw:%d", card);
	snd_ctl_t *ctl = NULL;
	int flags = desired >= 0 ? SND_CTL_NONBLOCK : SND_CTL_READONLY | SND_CTL_NONBLOCK;
	if (snd_ctl_open(&ctl, device, flags) < 0)
		return -1;
	snd_ctl_elem_list_t *list;
	snd_ctl_elem_info_t *info;
	snd_ctl_elem_value_t *value;
	snd_ctl_elem_id_t *id;
	snd_ctl_elem_list_alloca(&list);
	snd_ctl_elem_info_alloca(&info);
	snd_ctl_elem_value_alloca(&value);
	snd_ctl_elem_id_alloca(&id);
	int mode = -1;
	unsigned int count = 0;
	if (snd_ctl_elem_list(ctl, list) < 0 || (count = snd_ctl_elem_list_get_count(list)) > 1024)
		goto done;
	if (snd_ctl_elem_list_alloc_space(list, count) < 0)
		goto done;
	if (snd_ctl_elem_list(ctl, list) < 0)
		goto free_list;
	for (unsigned int at = 0; at < snd_ctl_elem_list_get_used(list); ++at) {
		snd_ctl_elem_list_get_id(list, at, id);
		if (*requested && strcmp(requested, snd_ctl_elem_id_get_name(id)) != 0)
			continue;
		snd_ctl_elem_info_clear(info);
		snd_ctl_elem_info_set_id(info, id);
		if (snd_ctl_elem_info(ctl, info) < 0 || !snd_ctl_elem_info_is_readable(info) ||
			snd_ctl_elem_info_get_count(info) != 1)
			continue;
		snd_ctl_elem_value_clear(value);
		snd_ctl_elem_value_set_id(value, id);
		if (snd_ctl_elem_read(ctl, value) < 0)
			continue;
		snd_ctl_elem_type_t type = snd_ctl_elem_info_get_type(info);
		if (*requested) {
			long selected = -1;
			if (type == SND_CTL_ELEM_TYPE_BOOLEAN)
				selected = snd_ctl_elem_value_get_boolean(value, 0);
			else if (type == SND_CTL_ELEM_TYPE_INTEGER &&
				snd_ctl_elem_info_get_min(info) == 0 && snd_ctl_elem_info_get_max(info) == 1)
				selected = snd_ctl_elem_value_get_integer(value, 0);
			else if (type == SND_CTL_ELEM_TYPE_ENUMERATED && snd_ctl_elem_info_get_items(info) == 2)
				selected = snd_ctl_elem_value_get_enumerated(value, 0);
			if (selected == 0 || selected == 1) {
				if (desired >= 0) {
					if (type == SND_CTL_ELEM_TYPE_ENUMERATED)
						snd_ctl_elem_value_set_enumerated(value, 0, desired);
					else if (type == SND_CTL_ELEM_TYPE_BOOLEAN)
						snd_ctl_elem_value_set_boolean(value, 0, desired);
					else
						snd_ctl_elem_value_set_integer(value, 0, desired);
					mode = snd_ctl_elem_write(ctl, value) < 0 ? -1 : desired;
				} else {
					mode = (int)selected;
				}
				break;
			}
			continue;
		}
		if (type != SND_CTL_ELEM_TYPE_ENUMERATED)
			continue;
		unsigned int items = snd_ctl_elem_info_get_items(info);
		if (items > 64)
			continue;
		unsigned int selected = snd_ctl_elem_value_get_enumerated(value, 0);
		int has_speaker = 0, has_earphone = 0, selected_mode = -1, complete = 1;
		for (unsigned int item = 0; item < items; ++item) {
			snd_ctl_elem_info_set_item(info, item);
			if (snd_ctl_elem_info(ctl, info) < 0) {
				complete = 0;
				break;
			}
			int item_mode = alsa_output_label(snd_ctl_elem_info_get_item_name(info));
			has_speaker |= item_mode == 0;
			has_earphone |= item_mode == 1;
			if (item == selected)
				selected_mode = item_mode;
		}
		if (complete && has_speaker && has_earphone && selected_mode >= 0) {
			if (desired >= 0) {
				for (unsigned int item = 0; item < items; ++item) {
					snd_ctl_elem_info_set_item(info, item);
					if (snd_ctl_elem_info(ctl, info) < 0 || alsa_output_label(snd_ctl_elem_info_get_item_name(info)) != desired)
						continue;
					snd_ctl_elem_value_set_enumerated(value, 0, item);
					mode = snd_ctl_elem_write(ctl, value) < 0 ? -1 : desired;
					break;
				}
			} else {
				mode = selected_mode;
			}
			break;
		}
	}
free_list:
	snd_ctl_elem_list_free_space(list);
done:
	snd_ctl_close(ctl);
	return mode;
}

int alsa_selected_output(int requested_card, const char *control) {
	if (requested_card >= 0)
		return card_output_mode(requested_card, control, -1);
	int card = -1;
	for (int scanned = 0; scanned < 32; ++scanned) {
		if (snd_card_next(&card) < 0 || card < 0)
			break;
		int mode = card_output_mode(card, control, -1);
		if (mode >= 0)
			return mode;
	}
	return -1;
}

int alsa_set_output(int requested_card, const char *control, int mode) {
	if (mode != 0 && mode != 1)
		return -1;
	if (requested_card >= 0)
		return card_output_mode(requested_card, control, mode) < 0 ? -1 : 0;
	int card = -1;
	for (int scanned = 0; scanned < 32; ++scanned) {
		if (snd_card_next(&card) < 0 || card < 0)
			break;
		if (card_output_mode(card, control, mode) >= 0)
			return 0;
	}
	return -1;
}
