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

/* --- speaker/headphone switching ----------------------------------------- */

/* Match TEST-ASSETS/plugin-snd.c: choose the highest of the first two ALSA
 * cards, then use the Headphone and Speaker simple-element playback switches. */
static int output_card(int requested_card) {
	if (requested_card >= 0)
		return requested_card;
	int card = -1, highest = -1;
	for (int scanned = 0; scanned < 2; ++scanned) {
		if (snd_card_next(&card) < 0 || card < 0)
			break;
		if (card > highest)
			highest = card;
	}
	return highest;
}

static snd_mixer_elem_t *find_output(snd_mixer_t *mixer, const char *name) {
	snd_mixer_selem_id_t *sid;
	snd_mixer_selem_id_alloca(&sid);
	snd_mixer_selem_id_set_index(sid, 0);
	snd_mixer_selem_id_set_name(sid, name);
	return snd_mixer_find_selem(mixer, sid);
}

static int playback_switch(snd_mixer_elem_t *elem, int *state) {
	if (elem == NULL || !snd_mixer_selem_has_playback_switch(elem))
		return -1;
	if (snd_mixer_selem_get_playback_switch(elem, SND_MIXER_SCHN_FRONT_LEFT, state) >= 0)
		return 0;
	return snd_mixer_selem_get_playback_switch(elem, SND_MIXER_SCHN_MONO, state);
}

static int open_output_mixer(int requested_card, snd_mixer_t **mixer,
			     snd_mixer_elem_t **headphone, snd_mixer_elem_t **speaker) {
	int card = output_card(requested_card);
	if (card < 0)
		return -1;
	char card_name[16];
	snprintf(card_name, sizeof(card_name), "hw:%d", card);
	if (snd_mixer_open(mixer, 0) < 0)
		return -1;
	if (snd_mixer_attach(*mixer, card_name) < 0 ||
	    snd_mixer_selem_register(*mixer, NULL, NULL) < 0 ||
	    snd_mixer_load(*mixer) < 0) {
		snd_mixer_close(*mixer);
		*mixer = NULL;
		return -1;
	}
	*headphone = find_output(*mixer, "Headphone");
	*speaker = find_output(*mixer, "Speaker");
	if (*headphone == NULL || *speaker == NULL) {
		snd_mixer_close(*mixer);
		*mixer = NULL;
		return -1;
	}
	return 0;
}

static snd_mixer_elem_t *selected_output_element(snd_mixer_elem_t *headphone,
						  snd_mixer_elem_t *speaker) {
	int hp = -1, sp = -1;
	if (playback_switch(headphone, &hp) < 0 || playback_switch(speaker, &sp) < 0)
		return NULL;
	if (hp)
		return headphone;
	if (sp)
		return speaker;
	return NULL;
}

int alsa_selected_output(int requested_card) {
	snd_mixer_t *mixer = NULL;
	snd_mixer_elem_t *headphone = NULL, *speaker = NULL;
	if (open_output_mixer(requested_card, &mixer, &headphone, &speaker) < 0)
		return -1;
	int hp = -1, sp = -1;
	int ok = playback_switch(headphone, &hp) >= 0 && playback_switch(speaker, &sp) >= 0;
	snd_mixer_close(mixer);
	if (!ok)
		return -1;
	if (hp && !sp)
		return 1;
	if (sp && !hp)
		return 0;
	/* If old state left both enabled, active headphones still mean earphone;
	 * if both are muted, neither route can be inferred. */
	return hp ? 1 : -1;
}

int alsa_set_output(int requested_card, int mode) {
	if (mode != 0 && mode != 1)
		return -1;
	snd_mixer_t *mixer = NULL;
	snd_mixer_elem_t *headphone = NULL, *speaker = NULL;
	if (open_output_mixer(requested_card, &mixer, &headphone, &speaker) < 0)
		return -1;
	/* plugin-snd.c mutes the current route before unmuting the next. */
	snd_mixer_elem_t *off = mode == 1 ? speaker : headphone;
	snd_mixer_elem_t *on = mode == 1 ? headphone : speaker;
	int ok = snd_mixer_selem_set_playback_switch_all(off, 0) >= 0 &&
		snd_mixer_selem_set_playback_switch_all(on, 1) >= 0;
	snd_mixer_close(mixer);
	return ok ? 0 : -1;
}

int alsa_set_volume(int requested_card, int percent) {
	if (percent < 0 || percent > 100)
		return -1;
	snd_mixer_t *mixer = NULL;
	snd_mixer_elem_t *headphone = NULL, *speaker = NULL;
	if (open_output_mixer(requested_card, &mixer, &headphone, &speaker) < 0)
		return -1;
	snd_mixer_elem_t *selected = selected_output_element(headphone, speaker);
	if (selected == NULL) {
		snd_mixer_close(mixer);
		return -1;
	}
	long minimum = 0, maximum = 0;
	snd_mixer_selem_get_playback_volume_range(selected, &minimum, &maximum);
	if (maximum <= minimum) {
		snd_mixer_close(mixer);
		return -1;
	}
	long volume = minimum + (long)((double)(maximum - minimum) * percent / 100.0);
	int ok = snd_mixer_selem_set_playback_volume_all(selected, volume) >= 0;
	snd_mixer_close(mixer);
	return ok ? 0 : -1;
}
