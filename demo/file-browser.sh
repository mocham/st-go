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
# fit-height. Directories/.. show a small hint instead.
preview() {
	local cur="${FILES[$IDX]}"
	if is_dir "$cur"; then
		if [ "$cur" = "$DIR/.." ]; then
			printf '\033[1;1H\033[K\033[7m [parent directory] \033[0m'
		else
			printf '\033[1;1H\033[K\033[7m [directory] \033[0m'
		fi
		return
	fi
	case "$cur" in
		*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp|*.pdf)
			show "$cur" "fit-height"
			;;
		*)
			# text file: render rows from the top-left corner
			printf '\033[1;1H'
			show "$cur" ""
			;;
	esac
}

# --- draw the right-panel file list ------------------------------------------
draw_list() {
	local y=1
	local i
	local maxlen=$(( LISTW - 2 ))   # room for the surrounding spaces
	for i in "${!FILES[@]}"; do
		[ "$y" -gt "$ROWS" ] && break
		local f="${FILES[$i]}"
		local name=$(base "$f")
		if is_dir "$f"; then
			name="$name/"
		fi
		# truncate long names so they never spill into the preview panel
		if [ "${#name}" -gt "$maxlen" ]; then
			name="${name:0:$maxlen}"
		fi
		printf '\033[%d;%dH\033[K' "$y" "$LISTX"
		if [ "$i" = "$IDX" ]; then
			printf '\033[7m %s \033[0m' "$name"
		else
			printf ' %s ' "$name"
		fi
		y=$(( y + 1 ))
	done
}

# --- render the whole browser ------------------------------------------------
render() {
	clear_screen
	dcs "setpwd '$DIR'"
	preview
	draw_list
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
		render
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
		render
	fi
}

# --- init ----------------------------------------------------------------------
build_list
render

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
						A) IDX=$(( (IDX - 1 + ${#FILES[@]}) % ${#FILES[@]} )); render ;;
						B) IDX=$(( (IDX + 1) % ${#FILES[@]} )); render ;;
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
