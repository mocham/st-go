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
#   -p, --page N       show page N (1-based) of a PDF (default: 1)
#
# Keys:
#   Right / Left        next / previous file
#   Up / Down           previous / next PDF page (when viewing a PDF),
#                       otherwise previous / next file
#   Home / End          first / last file
#   Esc or q            quit
#
# The script writes nothing to the terminal except the DCS image payloads,
# so it can be piped straight into st:  ./st -e ./demo/image-viewer.sh

set -u

# --- defaults ------------------------------------------------------------
FIT="fit-height"
DIR="."
PAGE=""
IMAGES=()

# --- option parsing -------------------------------------------------------
while [ $# -gt 0 ]; do
	case "$1" in
		-f|--fit-width)  FIT="fit-width" ;;
		-h|--fit-height) FIT="fit-height" ;;
		-n|--native)     FIT="" ;;
		-d|--dir)        shift; DIR="${1:-}" ;;
		-p|--page)       shift; PAGE="$1" ;;
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
		*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp|*.pdf) IMAGES+=("$f") ;;
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
# For PDFs, append "page N" (a 1-based counter; st wraps it modulo the page
# count, so the script only needs simple +/-1 arithmetic).
CURPAGE=1
page_arg() {
	case "$1" in
		*.pdf) printf ' page %s' "$CURPAGE" ;;
	esac
}
show() { printf '\033P%s\033\\' "open '$1' $FIT$(page_arg "$1")"; }
clear_screen() { printf '\033Pclear\033\\'; }

# is_pdf <file>: 0 if the path ends in .pdf (case-insensitive).
is_pdf() { case "$1" in *.pdf|*.PDF) return 0;; esac; return 1; }

# status bar on the last screen row (standard ANSI, works in any st):
#   [cur/total] <path> (page P)  (fit mode)
# The row is cleared and written with reverse video so it stands out.
rows=$(stty size < /dev/tty 2>/dev/null | awk '{print $1}')
rows=${rows:-24}
status() {
	local idx=$1 total=$2 path=$3
	local bar="[$((idx+1))/$total] $path  ($FIT)"
	if is_pdf "$path"; then
		bar="[$((idx+1))/$total] $path  page $CURPAGE ($FIT)"
	fi
	printf '\033[%d;1H\033[K\033[7m %s \033[0m' "$rows" "$bar"
}

# emit setpwd once so relative paths work regardless of st's own CWD
printf '\033P%s\033\\' "setpwd '$PWD'"

idx=0
n=${#IMAGES[@]}

# --- open <index>: show image i; for a PDF reset the page counter ----------
open_image() {
	local i=$1
	if is_pdf "${IMAGES[$i]}"; then
		CURPAGE=${PAGE:-1}   # -p sets the starting page (default 1)
		[ "$CURPAGE" -lt 1 ] && CURPAGE=1
	else
		CURPAGE=1
	fi
	show "${IMAGES[$i]}"
	status "$i" "$n" "${IMAGES[$i]}"
}

open_image 0

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
						A)  # Up: previous PDF page (mod count), else prev image
							if is_pdf "${IMAGES[$idx]}"; then
								CURPAGE=$(( CURPAGE - 1 ))
								show "${IMAGES[$idx]}"; status "$idx" "$n" "${IMAGES[$idx]}"
							else
								idx=$(( (idx - 1 + n) % n )); open_image "$idx"
							fi
							;;
						B)  # Down: next PDF page (mod count), else next image
							if is_pdf "${IMAGES[$idx]}"; then
								CURPAGE=$(( CURPAGE + 1 ))
								show "${IMAGES[$idx]}"; status "$idx" "$n" "${IMAGES[$idx]}"
							else
								idx=$(( (idx + 1) % n )); open_image "$idx"
							fi
							;;
						D)   idx=$(( (idx - 1 + n) % n )); open_image "$idx" ;;  # Left
						C)   idx=$(( (idx + 1) % n )); open_image "$idx" ;;  # Right
						H)   idx=0; open_image 0 ;;  # Home
						F)   idx=$(( n - 1 )); open_image "$idx" ;;  # End
						*)   clear_screen; break ;;
					esac
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
