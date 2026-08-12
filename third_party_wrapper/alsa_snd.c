/*
 * alsa_snd.c — minimal ALSA playback wrapper used by the Go file-browser's
 * mp3 viewer. Adapted from gowinmgr's CPlugins/plugin-snd.c:
 *
 *   - Opens the hardware device directly (hw:card,0) rather than the "default"
 *     PCM. "default" routes through dmix, whose config parse calls
 *     getgrnam_r (patched out for static linking), which breaks when ipc_gid
 *     is a group name. The direct hw device sidesteps that path entirely.
 *   - Installs an ALSA error handler (snd_lib_error_set_handler) that appends
 *     every SNDERR message to the file named by $ST_GO_ALSA_LOG instead of
 *     writing to stderr, so ALSA diagnostics never corrupt the terminal UI.
 *
 * Compiled once (like pdf_bridge.o) so the Go side only declares extern
 * functions and never needs the ALSA headers.
 */
#include <alsa/asoundlib.h>
#include <alsa/error.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

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
