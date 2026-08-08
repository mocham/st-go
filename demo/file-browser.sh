#!/usr/bin/env bash
# file-browser.sh - a mini file browser using st-go's DCS display DSL.
#
# Layout:
#   left  panel : preview of the current file (text rows, or image/PDF via
#                 fit-height), starting from the top row
#   right panel : list of entries in the current directory, ".." (parent) at
#                 the top, current entry highlighted
#
# Keys:
#   Up / Down       move the active file up / down
#   Right           enter the selected directory, or (for a file) show it
#   Left            go up to the parent directory if it exists
#   q / Esc         quit
#
# The script writes the DSL payloads and ANSI highlighting straight to the
# terminal:  ./st -e ./demo/file-browser.sh

set -u

DIR="${1:-$(pwd)}"
DIR="$(cd "$DIR" 2>/dev/null && pwd || echo "$DIR")"

# --- terminal size ----------------------------------------------------------
TS=$(stty size < /dev/tty 2>/dev/null || echo "24 80")
ROWS=${TS%% *}
COLS=${TS##* }
LISTW=30
LISTX=$(( COLS - LISTW + 1 ))
[ "$LISTX" -lt 1 ] && LISTX=1

# --- state -------------------------------------------------------------------
FILES=()
IDX=0

# --- DSL helper (DCS = ESC P <payload> ESC \) --------------------------------
dcs() { printf '\033P%s\033\\' "$1"; }
show() { dcs "open '$1' $2"; }
clear_screen() { dcs "clear"; }

# is_dir <path>
is_dir() { [ -d "$1" ]; }

# is_image <path>: file types rendered with fit-height (which clears the screen)
is_image() {
	case "$1" in
		*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp|*.pdf) return 0 ;;
		*) return 1 ;;
	esac
}

# basename helper (portable)
base() { local p=$1; p=${p%/}; printf '%s' "${p##*/}"; }

# --- build the entry list for $DIR into $FILES -------------------------------
# ".." is always the first entry when a parent exists.
build_list() {
	FILES=()
	if [ "$DIR" != "/" ]; then
		FILES+=("$DIR/..")
	fi
	for f in "$DIR"/*; do
		[ -e "$f" ] || continue
		FILES+=("$f")
	done
}

# --- preview the current entry (left panel) ----------------------------------
# Text files render via the text DSL (rows from the top); images/PDFs via
# fit-height. Directories/.. show a small hint instead. The preview area is
# cleared first so an incremental redraw never leaves stale text behind.
# NOTE: ECH (\033[nX) is used (not EL \033[K) so only columns 1..LISTX-1 are
# erased; \033[K would wipe to the end of the line and blank the file list.
preview() {
	local py
	local pw=$(( LISTX - 1 ))
	[ "$pw" -lt 1 ] && pw=1
	for py in $(seq 1 $ROWS); do
		printf '\033[%d;1H\033[%dX' "$py" "$pw"
	done
	local cur="${FILES[$IDX]}"
	if is_dir "$cur"; then
		if [ "$cur" = "$DIR/.." ]; then
			printf '\033[1;1H\033[7m [parent directory] \033[0m'
		else
			printf '\033[1;1H\033[7m [directory] \033[0m'
		fi
		return
	fi
	if is_image "$cur"; then
		show "$cur" "fit-height"
	else
		# text file: render rows from the top-left corner
		printf '\033[1;1H'
		show "$cur" ""
	fi
}

# --- draw the right-panel file list ------------------------------------------
# draw_row <index>: redraw only that list row (cheap; used for selection moves).
draw_row() {
	local i=$1
	local y=$(( i + 1 ))
	[ "$y" -gt "$ROWS" ] && return
	local f="${FILES[$i]}"
	local name=$(base "$f")
	local maxlen=$(( LISTW - 2 ))
	if is_dir "$f"; then
		name="$name/"
	fi
	if [ "${#name}" -gt "$maxlen" ]; then
		name="${name:0:$maxlen}"
	fi
	printf '\033[%d;%dH\033[K' "$y" "$LISTX"
	if [ "$i" = "$IDX" ]; then
		printf '\033[7m %s \033[0m' "$name"
	else
		printf ' %s ' "$name"
	fi
}

draw_list() {
	local i
	for i in "${!FILES[@]}"; do
		draw_row "$i"
	done
}

# --- render the whole browser (init / directory change) ----------------------
render_all() {
	clear_screen
	dcs "setpwd '$DIR'"
	preview
	draw_list
}

# --- move the active selection by `delta` ------------------------------------
# Only the previously-active and newly-active list rows are redrawn, and the
# preview is refreshed only when the selected file actually changed. The list
# rows are redrawn after the preview because fit-height images clear the whole
# screen; in that case the full list is redrawn too.
move_selection() {
	local delta=$1
	local old=$IDX
	IDX=$(( (IDX + delta + ${#FILES[@]}) % ${#FILES[@]} ))
	if [ "$IDX" != "$old" ]; then
		preview
		if is_image "${FILES[$IDX]}"; then
			draw_list
		else
			draw_row "$old"
			draw_row "$IDX"
		fi
	fi
}

# --- navigation ---------------------------------------------------------------
# Right: enter a directory (or re-preview a file)
right() {
	local cur="${FILES[$IDX]}"
	if is_dir "$cur"; then
		if [ "$cur" = "$DIR/.." ]; then
			left
			return
		fi
		DIR="$cur"
		build_list
		IDX=0
		render_all
	fi
	# for a file, it is already previewed
}

# Left: go to the parent directory if it exists
left() {
	local parent
	if [ "$DIR" != "/" ]; then
		parent=$(dirname "$DIR")
		DIR="$parent"
		build_list
		IDX=0
		render_all
	fi
}

# --- init ----------------------------------------------------------------------
build_list
render_all

# --- input loop -----------------------------------------------------------------
while :; do
	IFS= read -r -s -n 1 key || break
	case "$key" in
		q|Q)
			clear_screen
			break
			;;
		$'\e')
			IFS= read -r -s -n 1 -t 0.05 seq || { clear_screen; break; }
			case "$seq" in
				'[')
					IFS= read -r -s -n 1 fin || { clear_screen; break; }
					case "$fin" in
						A) move_selection -1 ;;
						B) move_selection 1 ;;
						C) right ;;
						D) left ;;
						*) clear_screen; break ;;
					esac
					;;
				*) clear_screen; break ;;
			esac
			;;
	esac
done

clear_screen
