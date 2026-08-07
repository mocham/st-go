#!/usr/bin/env bash
# image-viewer.sh - view images in the st terminal using its DCS image DSL.
#
# Usage:
#   image-viewer.sh [options] [path]
#
# Options:
#   -f, --fit-width    scale images to the terminal width
#   -h, --fit-height   scale images to the terminal height (default)
#   -n, --native       show images at native size (no fit)
#   -d, --dir PATH     view images in PATH (default: current directory)
#
# Keys:
#   Right / Down       next image
#   Left / Up          previous image
#   Esc or q           quit
#
# The script writes nothing to the terminal except the DCS image payloads,
# so it can be piped straight into st:  ./st -e ./demo/image-viewer.sh

set -u

# --- defaults ------------------------------------------------------------
FIT="fit-height"
DIR="."
IMAGES=()

# --- option parsing -------------------------------------------------------
while [ $# -gt 0 ]; do
	case "$1" in
		-f|--fit-width)  FIT="fit-width" ;;
		-h|--fit-height) FIT="fit-height" ;;
		-n|--native)     FIT="" ;;
		-d|--dir)        shift; DIR="${1:-}" ;;
		--)              shift; break ;;
		-*)              echo "unknown option: $1" >&2; exit 1 ;;
		*)               DIR="$1" ;;
	esac
	shift
done

if [ -z "$DIR" ]; then
	DIR="."
fi

# --- collect images (sorted, like ls) --------------------------------------
for f in "$DIR"/*; do
	[ -e "$f" ] || continue
	case "$f" in
		*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp) IMAGES+=("$f") ;;
	esac
done

if [ ${#IMAGES[@]} -eq 0 ]; then
	echo "no images found in $DIR" >&2
	exit 1
fi

# --- helpers ---------------------------------------------------------------
# esc P <stmt> ESC \  (DCS image DSL)
# Tell st the shell's PWD so relative image paths resolve; then send the path
# as-is (the shell expands globs, st just reads the file).
show() { printf '\033P%s\033\\' "open '$1' $FIT"; }
clear_screen() { printf '\033Pclear\033\\'; }

# status bar on the last screen row (standard ANSI, works in any st):
#   [cur/total] <path>  (fit mode)
# The row is cleared and written with reverse video so it stands out.
rows=$(stty size < /dev/tty 2>/dev/null | awk '{print $1}')
rows=${rows:-24}
status() {
	local idx=$1 total=$2 path=$3
	local bar="[$((idx+1))/$total] $path  ($FIT)"
	printf '\033[%d;1H\033[K\033[7m %s \033[0m' "$rows" "$bar"
}

# emit setpwd once so relative paths work regardless of st's own CWD
printf '\033P%s\033\\' "setpwd '$PWD'"

idx=0
n=${#IMAGES[@]}

show "${IMAGES[$idx]}"
status 0 "$n" "${IMAGES[$idx]}"

# --- input loop -------------------------------------------------------------
while :; do
	IFS= read -r -s -n 1 key || break
	case "$key" in
		q|Q)  # q quits
			clear_screen
			break
			;;
		$'\e')  # ESC: either a lone Esc (quit) or an arrow sequence
			# peek: read the rest of the escape sequence if any
			IFS= read -r -s -n 1 -t 0.05 seq || { clear_screen; break; }
			case "$seq" in
				'[')  # CSI
					IFS= read -r -s -n 1 fin || { clear_screen; break; }
					case "$fin" in
						A|H) idx=$(( (idx - 1 + n) % n )) ;;  # Up / Home
						B|F) idx=$(( (idx + 1) % n )) ;;  # Down / End
						D)   idx=$(( (idx - 1 + n) % n )) ;;  # Left
						C)   idx=$(( (idx + 1) % n )) ;;  # Right
						*)   clear_screen; break ;;
					esac
					show "${IMAGES[$idx]}"
					status "$idx" "$n" "${IMAGES[$idx]}"
					;;
				*)  # bare Esc or unrecognized
					clear_screen
					break
					;;
			esac
			;;
	esac
done

clear_screen
