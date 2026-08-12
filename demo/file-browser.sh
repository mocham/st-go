#!/usr/bin/env bash
# A pane-based file browser for st-go's built-in text, image, and PDF display.
#
# Usage:
#   file-browser.sh [--hidden] [--open SHELL_CODE] [directory]
#
# SHELL_CODE is run by `bash -c` with FILE, NAME, and BROWSER_DIR exported.
# This permits functions and command sequences, for example:
#   --open 'case $FILE in *.pdf) zathura "$FILE";; *) xdg-open "$FILE";; esac'

set -u

if [[ -v ST_FILE_BROWSER_OPEN ]]; then
	OPEN_CMD=$ST_FILE_BROWSER_OPEN
else
	OPEN_CMD='
editor=${ST_GO_EXECUTABLE:-st}
case ${FILE,,} in
  *.txt|*.md|*.markdown|*.html|*.htm|*.css|*.tex|*.bib|*.bbl|*.py|*.c|*.h|*.cc|*.cpp|*.cxx|*.go|*.rs|*.java|*.js|*.jsx|*.ts|*.tsx|*.sh|*.bash|*.zsh|*.yml|*.yaml|*.json|*.toml|*.ini|*.conf|*.xml|*.csv|*.log|*.rst)
    "$editor" vim "$FILE"
    ;;
  *)
    xdg-open "$FILE"
    ;;
esac'
fi
SHOW_HIDDEN=${ST_FILE_BROWSER_HIDDEN:-0}
START_DIR=

case ${SHOW_HIDDEN,,} in
	1|true|yes|on) SHOW_HIDDEN=1 ;;
	*) SHOW_HIDDEN=0 ;;
esac

usage() {
	cat <<'EOF'
Usage: file-browser.sh [options] [directory]

Options:
  -a, --hidden       include hidden entries
  -o, --open CODE    run CODE on a file double-click or Enter
  -h, --help         show this help

CODE is evaluated by `bash -c` with FILE, NAME, and BROWSER_DIR exported.
It may contain a complete shell command sequence or function definitions.

Keys: arrows navigate, Enter opens/enters, Backspace goes up, PgUp/PgDn
scroll, [ and ] change PDF pages, . toggles hidden files, r refreshes, q quits.
Mouse: click selects, double-click opens/enters, wheel scrolls the list; over a
PDF preview the wheel changes pages.
EOF
}

while (($#)); do
	case $1 in
		-a|--hidden) SHOW_HIDDEN=1 ;;
		-o|--open)
			shift
			if (($# == 0)); then
				printf '%s\n' 'file-browser.sh: --open requires shell code' >&2
				exit 2
			fi
			OPEN_CMD=$1
			;;
		--open=*) OPEN_CMD=${1#*=} ;;
		-h|--help) usage; exit 0 ;;
		--)
			shift
			if (($# > 1)); then
				printf '%s\n' 'file-browser.sh: only one directory may be specified' >&2
				exit 2
			fi
			START_DIR=${1:-}
			break
			;;
		-*) printf 'file-browser.sh: unknown option: %s\n' "$1" >&2; exit 2 ;;
		*)
			if [[ -n $START_DIR ]]; then
				printf '%s\n' 'file-browser.sh: only one directory may be specified' >&2
				exit 2
			fi
			START_DIR=$1
			;;
	esac
	shift
done

START_DIR=${START_DIR:-$PWD}
if ! DIR=$(cd -- "$START_DIR" 2>/dev/null && pwd -P); then
	printf 'file-browser.sh: cannot open directory: %s\n' "$START_DIR" >&2
	exit 1
fi

# Terminal/UI state.
ROWS=24
COLS=80
LIST_W=32
LIST_X=49
PREVIEW_W=47
LIST_TOP=8
LIST_BOTTOM=22
VISIBLE=15
IDX=0
VIEW_TOP=0
PAGE=1
FILES=()
STATUS='Ready'
PREVIEW_WAS_GRAPHIC=0
PREVIEW_FULL_REDRAW=0
ATLAS_CLEAN=1
COMPACT=0
LAST_CLICK_IDX=-1
LAST_CLICK_MS=0
DOUBLE_CLICK_MS=${ST_FILE_BROWSER_DOUBLE_CLICK_MS:-350}
[[ $DOUBLE_CLICK_MS =~ ^[0-9]+$ ]] || DOUBLE_CLICK_MS=350
((DOUBLE_CLICK_MS < 50)) && DOUBLE_CLICK_MS=50
((DOUBLE_CLICK_MS > 2000)) && DOUBLE_CLICK_MS=2000
RESIZED=0
CLEANED=0
PATH_ACTIVE=0
PATH_ABORT=0
PATH_BUFFER=
PATH_CURSOR=0
PATH_MATCHES=()
PATH_MATCH_IDX=-1
PATH_MATCH_TOP=0
PATH_MESSAGE=
PATH_POPUP_MAX=8

CACHE_DIR=$(mktemp -d /tmp/st-go-browser.XXXXXX) || exit 1
PREVIEW_LINK=$CACHE_DIR/content
GEOMETRY_TAG=file-browser-$$

icon_value() {
	local key=$1 fallback=$2 user=ST_FILE_BROWSER_ICON_$1 generated=ST_GO_FILE_BROWSER_ICON_$1
	if [[ -v $user ]]; then REPLY=${!user}
	elif [[ -v $generated ]]; then REPLY=${!generated}
	else REPLY=$fallback
	fi
}
icon_value PARENT '↑'; ICON_PARENT=$REPLY
icon_value DIRECTORY '▸'; ICON_DIRECTORY=$REPLY
icon_value SYMLINK '↗'; ICON_SYMLINK=$REPLY
icon_value IMAGE '▣'; ICON_IMAGE=$REPLY
icon_value PDF '▤'; ICON_PDF=$REPLY
icon_value TEXT '≡'; ICON_TEXT=$REPLY
icon_value ARCHIVE '▦'; ICON_ARCHIVE=$REPLY
icon_value AUDIO '♪'; ICON_AUDIO=$REPLY
icon_value VIDEO '▶'; ICON_VIDEO=$REPLY
icon_value CODE 'λ'; ICON_CODE=$REPLY
icon_value CONFIG '⚙'; ICON_CONFIG=$REPLY
icon_value EXECUTABLE '◆'; ICON_EXECUTABLE=$REPLY
icon_value DEFAULT '·'; ICON_DEFAULT=$REPLY

RESET=$'\033[0m'
DIM=$'\033[2m'
HEADER=$'\033[38;5;231m\033[48;5;24m\033[1m'
SELECTED=$'\033[38;5;231m\033[48;5;31m\033[1m'
DIR_STYLE=$'\033[38;5;81m\033[1m'
IMAGE_STYLE=$'\033[38;5;213m'
PDF_STYLE=$'\033[38;5;203m'
INFO_STYLE=$'\033[38;5;110m'
STATUS_STYLE=$'\033[38;5;254m\033[48;5;236m'

dcs() { printf '\033P%s\033\\' "$1"; }
window_dcs() {
	if [[ -n ${ST_GO_GEOMETRY_TOKEN:-} ]]; then
		dcs "window auth $ST_GO_GEOMETRY_TOKEN $*"
	else
		dcs "window $*"
	fi
}
cup() { printf '\033[%d;%dH' "$1" "$2"; }

cleanup() {
	((CLEANED)) && return
	CLEANED=1
	window_dcs forget "$GEOMETRY_TAG"
	printf '\033[?1006l\033[?1000l\033[?25h\033[0m\033[?1049l'
	rm -rf -- "$CACHE_DIR"
}
trap cleanup EXIT
interrupt_browser() {
	if ((PATH_ACTIVE)); then PATH_ABORT=1; else exit 130; fi
}
trap interrupt_browser INT
trap 'exit 130' TERM HUP
trap 'RESIZED=1' WINCH

terminal_size() {
	local size
	size=$(stty size 2>/dev/null || printf '24 80')
	ROWS=${size%% *}
	COLS=${size##* }
	[[ $ROWS =~ ^[0-9]+$ ]] || ROWS=24
	[[ $COLS =~ ^[0-9]+$ ]] || COLS=80
	COMPACT=0
	if ((ROWS < 12 || COLS < 44)); then
		COMPACT=1
		return
	fi

	LIST_W=${ST_FILE_BROWSER_LIST_WIDTH:-34}
	[[ $LIST_W =~ ^[0-9]+$ ]] || LIST_W=34
	((LIST_W < 24)) && LIST_W=24
	((LIST_W > COLS / 2)) && LIST_W=$((COLS / 2))
	LIST_X=$((COLS - LIST_W + 1))
	PREVIEW_W=$((LIST_X - 2))
	LIST_BOTTOM=$((ROWS - 2))
	VISIBLE=$((LIST_BOTTOM - LIST_TOP + 1))
	((VISIBLE < 1)) && VISIBLE=1
}

display_text() {
	printf -v REPLY '%q' "$1"
}

shorten() {
	local text=$1 width=$2
	if ((width <= 0)); then
		REPLY=
	elif ((${#text} > width)); then
		if ((width > 3)); then
			REPLY=${text:0:width-3}'...'
		else
			REPLY=${text:0:width}
		fi
	else
		REPLY=$text
	fi
}

draw_text() {
	local row=$1 col=$2 width=$3 style=$4 text=$5
	((row < 1 || row > ROWS || width < 1)) && return
	shorten "$text" "$width"
	cup "$row" "$col"
	printf '\033[%dX%s%s%s' "$width" "$style" "$REPLY" "$RESET"
}

base_name() {
	local path=${1%/}
	REPLY=${path##*/}
	[[ -n $REPLY ]] || REPLY=/
}

is_graphic() {
	local path=${1,,}
	case $path in
		*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp|*.pdf) return 0 ;;
	esac
	return 1
}

is_pdf() {
	[[ ${1,,} == *.pdf ]]
}

file_type() {
	local path=$1 ext=${1,,}
	if [[ $path == "$DIR/.." ]]; then REPLY=parent
	elif [[ -L $path ]]; then REPLY=symlink
	elif [[ -d $path ]]; then REPLY=directory
	else
		case $ext in
			*.pdf) REPLY=pdf ;;
			*.png|*.jpg|*.jpeg|*.gif|*.bmp|*.tga|*.webp|*.svg) REPLY=image ;;
			*.zip|*.tar|*.tgz|*.gz|*.bz2|*.xz|*.7z|*.rar) REPLY=archive ;;
			*.mp3|*.wav|*.flac|*.ogg|*.m4a|*.aac) REPLY=audio ;;
			*.mp4|*.mkv|*.webm|*.avi|*.mov|*.mpeg|*.mpg) REPLY=video ;;
			*.go|*.c|*.h|*.cc|*.cpp|*.cxx|*.rs|*.py|*.js|*.ts|*.tsx|*.jsx|*.java|*.sh|*.bash|*.zsh) REPLY=code ;;
			*.json|*.yaml|*.yml|*.toml|*.ini|*.conf|*.xml) REPLY=config ;;
			*.txt|*.md|*.markdown|*.html|*.htm|*.css|*.tex|*.bib|*.bbl|*.rst|*.log|*.csv) REPLY=text ;;
			*) if [[ -x $path ]]; then REPLY=executable; else REPLY=default; fi ;;
		esac
	fi
}

is_text_file() {
	local mime
	if command -v file >/dev/null 2>&1; then
		mime=$(file -Lb --mime-type -- "$1" 2>/dev/null || true)
		case $mime in
			text/*|application/json|application/xml|application/x-shellscript|application/javascript) return 0 ;;
		esac
		return 1
	fi
	LC_ALL=C grep -Iq . -- "$1" 2>/dev/null
}

human_size() {
	local n=${1:-0} unit=B
	if ((n >= 1073741824)); then n=$((n / 1073741824)); unit=GiB
	elif ((n >= 1048576)); then n=$((n / 1048576)); unit=MiB
	elif ((n >= 1024)); then n=$((n / 1024)); unit=KiB
	fi
	REPLY="$n $unit"
}

build_list() {
	local f name old_dotglob
	local -a dirs=() regular=()
	FILES=()
	[[ $DIR != / ]] && FILES+=("$DIR/..")
	old_dotglob=$(shopt -p dotglob || true)
	if ((SHOW_HIDDEN)); then shopt -s dotglob; else shopt -u dotglob; fi
	shopt -s nullglob
	for f in "$DIR"/*; do
		name=${f##*/}
		((SHOW_HIDDEN)) || [[ $name != .* ]] || continue
		if [[ -d $f ]]; then dirs+=("$f"); else regular+=("$f"); fi
	done
	eval "$old_dotglob"
	FILES+=("${dirs[@]}" "${regular[@]}")
	shopt -u nullglob
	((${#FILES[@]})) || IDX=-1
}

ensure_visible() {
	local count=${#FILES[@]}
	((count == 0)) && { IDX=-1; VIEW_TOP=0; return; }
	((IDX < 0)) && IDX=0
	((IDX >= count)) && IDX=$((count - 1))
	((IDX < VIEW_TOP)) && VIEW_TOP=$IDX
	((IDX >= VIEW_TOP + VISIBLE)) && VIEW_TOP=$((IDX - VISIBLE + 1))
	local max_top=$((count - VISIBLE))
	((max_top < 0)) && max_top=0
	((VIEW_TOP > max_top)) && VIEW_TOP=$max_top
	((VIEW_TOP < 0)) && VIEW_TOP=0
}

INFO_NAME='(empty directory)'
INFO_KIND=directory
INFO_SIZE='-'
INFO_MODE='-'
INFO_TIME='-'

update_info() {
	local path mime bytes stamp
	if ((IDX < 0 || IDX >= ${#FILES[@]})); then
		INFO_NAME='(empty directory)'; INFO_KIND=directory; INFO_SIZE=-; INFO_MODE=-; INFO_TIME=-
		return
	fi
	path=${FILES[$IDX]}
	base_name "$path"; display_text "$REPLY"; INFO_NAME=$REPLY
	if [[ $path == "$DIR/.." ]]; then
		INFO_KIND='parent directory'
	elif [[ -d $path ]]; then
		INFO_KIND=directory
	elif is_pdf "$path"; then
		INFO_KIND="PDF document, page $PAGE"
	elif is_graphic "$path"; then
		INFO_KIND=image
	elif command -v file >/dev/null 2>&1; then
		mime=$(file -Lb --mime-type -- "$path" 2>/dev/null || true)
		INFO_KIND=${mime:-file}
	else
		INFO_KIND=file
	fi
	bytes=$(stat -Lc %s -- "$path" 2>/dev/null || printf 0)
	human_size "$bytes"; INFO_SIZE=$REPLY
	INFO_MODE=$(stat -Lc %A -- "$path" 2>/dev/null || printf -- '-')
	stamp=$(stat -Lc %y -- "$path" 2>/dev/null || true)
	INFO_TIME=${stamp:0:16}
	[[ -n $INFO_TIME ]] || INFO_TIME=-
}

entry_label() {
	local path=$1 marker icon type name
	base_name "$path"; name=$REPLY
	file_type "$path"; type=$REPLY
	case $type in
		parent) icon=$ICON_PARENT ;;
		directory) icon=$ICON_DIRECTORY ;;
		symlink) icon=$ICON_SYMLINK ;;
		image) icon=$ICON_IMAGE ;;
		pdf) icon=$ICON_PDF ;;
		text) icon=$ICON_TEXT ;;
		archive) icon=$ICON_ARCHIVE ;;
		audio) icon=$ICON_AUDIO ;;
		video) icon=$ICON_VIDEO ;;
		code) icon=$ICON_CODE ;;
		config) icon=$ICON_CONFIG ;;
		executable) icon=$ICON_EXECUTABLE ;;
		*) icon=$ICON_DEFAULT ;;
	esac
	marker="$icon  "
	display_text "$name"
	REPLY=$marker$REPLY
}

entry_style() {
	local path=$1
	if [[ -d $path ]]; then REPLY=$DIR_STYLE
	elif is_pdf "$path"; then REPLY=$PDF_STYLE
	elif is_graphic "$path"; then REPLY=$IMAGE_STYLE
	else REPLY=$RESET
	fi
}

draw_header() {
	local path count=${#FILES[@]}
	display_text "$DIR"; path=$REPLY
	printf '\033[1;1H%s%*s\033[1;2H' "$HEADER" "$COLS" ''
	shorten "st-go files  $path" "$((COLS - 14))"
	printf '%s' "$REPLY"
	printf '\033[1;%dH%4d items%s' "$((COLS - 11))" "$count" "$RESET"
}

draw_info() {
	draw_text 2 "$LIST_X" "$LIST_W" "$INFO_STYLE" ' SELECTED'
	draw_text 3 "$LIST_X" "$LIST_W" "$SELECTED" " $INFO_NAME"
	draw_text 4 "$LIST_X" "$LIST_W" "$RESET" " type  $INFO_KIND"
	draw_text 5 "$LIST_X" "$LIST_W" "$RESET" " size  $INFO_SIZE   $INFO_MODE"
	draw_text 6 "$LIST_X" "$LIST_W" "$DIM" " time  $INFO_TIME"
	local first=0 last=0 count=${#FILES[@]}
	if ((count)); then first=$((VIEW_TOP + 1)); last=$((VIEW_TOP + VISIBLE)); ((last > count)) && last=$count; fi
	draw_text 7 "$LIST_X" "$LIST_W" "$INFO_STYLE" " FILES  $first-$last / $count"
}

draw_list_row() {
	local slot=$1 row index
	row=$((LIST_TOP + slot))
	index=$((VIEW_TOP + slot))
	((row > LIST_BOTTOM)) && return
	if ((index >= ${#FILES[@]})); then
		draw_text "$row" "$LIST_X" "$LIST_W" "$RESET" ''
		if ((index == 0 && ${#FILES[@]} == 0)); then
			draw_text "$row" "$LIST_X" "$LIST_W" "$DIM" ' (empty)'
		fi
		return
	fi
	local label style path=${FILES[$index]}
	entry_label "$path"; label=" $REPLY"
	if ((index == IDX)); then style=$SELECTED; else entry_style "$path"; style=$REPLY; fi
	draw_text "$row" "$LIST_X" "$LIST_W" "$style" "$label"
}

draw_list() {
	local slot
	for ((slot=0; slot<VISIBLE; slot++)); do draw_list_row "$slot"; done
}

draw_divider() {
	local row col=$((LIST_X - 1))
	for ((row=2; row<ROWS; row++)); do
		cup "$row" "$col"; printf '%s|%s' "$DIM" "$RESET"
	done
}

draw_status() {
	local help='  arrows navigate  Enter open  . hidden  r refresh  q quit'
	printf '\033[%d;1H%s%*s\033[%d;2H' "$ROWS" "$STATUS_STYLE" "$COLS" '' "$ROWS"
	shorten "$STATUS" "$((COLS - ${#help} - 3))"; printf '%s' "$REPLY"
	if ((COLS >= 76)); then printf '\033[%d;%dH%s' "$ROWS" "$((COLS - ${#help} + 1))" "$help"; fi
	printf '%s' "$RESET"
}

clear_path_popup() {
	local row first=$((ROWS - PATH_POPUP_MAX))
	((first < 2)) && first=2
	for ((row=first; row<ROWS; row++)); do
		cup "$row" 1; printf '\033[%dX' "$COLS"
	done
}

path_display_name() {
	local path=$1
	if [[ $PATH_BUFFER != /* && $path == "$DIR/"* ]]; then path=${path#"$DIR/"}; fi
	[[ -d $path ]] && path+=/
	display_text "$path"
}

draw_path_prompt() {
	local count=${#PATH_MATCHES[@]} visible=$PATH_POPUP_MAX start row i path style before after shown
	clear_path_popup
	if ((count < visible)); then visible=$count; fi
	if ((PATH_MATCH_IDX >= 0)); then
		((PATH_MATCH_IDX < PATH_MATCH_TOP)) && PATH_MATCH_TOP=$PATH_MATCH_IDX
		((PATH_MATCH_IDX >= PATH_MATCH_TOP + PATH_POPUP_MAX)) && PATH_MATCH_TOP=$((PATH_MATCH_IDX - PATH_POPUP_MAX + 1))
	fi
	((PATH_MATCH_TOP + visible > count)) && PATH_MATCH_TOP=$((count - visible))
	((PATH_MATCH_TOP < 0)) && PATH_MATCH_TOP=0
	start=$((ROWS - visible))
	for ((i=0; i<visible; i++)); do
		row=$((start + i))
		path=${PATH_MATCHES[$((PATH_MATCH_TOP + i))]}
		path_display_name "$path"; shown=" $REPLY"
		if ((PATH_MATCH_TOP + i == PATH_MATCH_IDX)); then style=$SELECTED; else style=$STATUS_STYLE; fi
		draw_text "$row" 1 "$COLS" "$style" "$shown"
	done
	before=${PATH_BUFFER:0:PATH_CURSOR}
	after=${PATH_BUFFER:PATH_CURSOR}
	display_text "$before"; before=$REPLY
	display_text "$after"; after=$REPLY
	shown=" / $before${SELECTED} ${RESET}${STATUS_STYLE}$after"
	if [[ -n $PATH_MESSAGE ]]; then shown+="  [$PATH_MESSAGE]"; fi
	draw_text "$ROWS" 1 "$COLS" "$STATUS_STYLE" "$shown"
}

path_pattern() {
	local input=$1 completion=$2
	if [[ $input == /* ]]; then REPLY=$input; else REPLY=$DIR/$input; fi
	if ((completion)) && ! path_has_glob "$input"; then REPLY+='*'; fi
}

path_has_glob() { [[ $1 == *'*'* || $1 == *'?'* || $1 == *'['* ]]; }

update_path_matches() {
	local completion=${1:-1} pattern f old_opts old_globignore_set=0 old_globignore=
	path_pattern "$PATH_BUFFER" "$completion"; pattern=$REPLY
	old_opts=$(shopt -p nullglob failglob dotglob nocaseglob globstar extglob || true)
	if [[ -v GLOBIGNORE ]]; then old_globignore_set=1; old_globignore=$GLOBIGNORE; fi
	unset GLOBIGNORE
	shopt -s nullglob
	shopt -u failglob dotglob nocaseglob globstar extglob
	local IFS=
	# shellcheck disable=SC2206 # Intentional glob expansion with IFS disabled.
	local -a expanded=( $pattern )
	eval "$old_opts"
	if ((old_globignore_set)); then GLOBIGNORE=$old_globignore; else unset GLOBIGNORE; fi
	PATH_MATCHES=()
	for f in "${expanded[@]}"; do
		[[ -e $f || -L $f ]] || continue
		PATH_MATCHES+=("$f")
	done
	PATH_MATCH_IDX=-1
	PATH_MATCH_TOP=0
	PATH_MESSAGE=
}

path_insert() {
	local text=$1
	PATH_BUFFER=${PATH_BUFFER:0:PATH_CURSOR}$text${PATH_BUFFER:PATH_CURSOR}
	PATH_CURSOR=$((PATH_CURSOR + ${#text}))
	update_path_matches 1
}

path_backspace() {
	((PATH_CURSOR > 0)) || return
	PATH_BUFFER=${PATH_BUFFER:0:PATH_CURSOR-1}${PATH_BUFFER:PATH_CURSOR}
	PATH_CURSOR=$((PATH_CURSOR - 1))
	update_path_matches 1
}

path_delete() {
	((PATH_CURSOR < ${#PATH_BUFFER})) || return
	PATH_BUFFER=${PATH_BUFFER:0:PATH_CURSOR}${PATH_BUFFER:PATH_CURSOR+1}
	update_path_matches 1
}

path_select_delta() {
	local delta=$1 count=${#PATH_MATCHES[@]}
	((count)) || return
	if ((PATH_MATCH_IDX < 0)); then
		if ((delta < 0)); then PATH_MATCH_IDX=$((count - 1)); else PATH_MATCH_IDX=0; fi
	else
		PATH_MATCH_IDX=$((PATH_MATCH_IDX + delta))
		((PATH_MATCH_IDX < 0)) && PATH_MATCH_IDX=0
		((PATH_MATCH_IDX >= count)) && PATH_MATCH_IDX=$((count - 1))
	fi
}

clear_preview() {
	local row
	for ((row=2; row<ROWS; row++)); do
		cup "$row" 1; printf '\033[%dX' "$PREVIEW_W"
	done
}

draw_preview_title() {
	draw_text 2 2 "$((PREVIEW_W - 2))" "$INFO_STYLE" "$1"
}

draw_preview_message() {
	draw_preview_title "$1"
	draw_text 4 3 "$((PREVIEW_W - 4))" "$RESET" "$2"
	[[ -n ${3:-} ]] && draw_text 6 3 "$((PREVIEW_W - 4))" "$DIM" "$3"
}

render_preview() {
	PREVIEW_FULL_REDRAW=0
	local path height width title
	if ((IDX < 0 || IDX >= ${#FILES[@]})); then
		clear_preview
		draw_preview_message 'EMPTY DIRECTORY' 'No entries to preview.' ''
		return
	fi
	path=${FILES[$IDX]}
	if is_graphic "$path" && [[ -f $path ]]; then
		clear_preview
		ln -sfn -- "$path" "$PREVIEW_LINK"
		base_name "$path"; display_text "$REPLY"
		draw_preview_title "PREVIEW  $REPLY"
		height=$((ROWS - 3)); ((height < 1)) && height=1
		if is_pdf "$path"; then
			dcs "open '$PREVIEW_LINK' rect 1 3 $PREVIEW_W $height fit-contain page $PAGE"
		else
			dcs "open '$PREVIEW_LINK' rect 1 3 $PREVIEW_W $height fit-contain"
		fi
		ATLAS_CLEAN=0
		PREVIEW_WAS_GRAPHIC=1
		return
	fi

	PREVIEW_WAS_GRAPHIC=0
	clear_preview
	base_name "$path"; display_text "$REPLY"; title=$REPLY
	if [[ -d $path ]]; then
		draw_preview_message 'DIRECTORY' "$title" 'Double-click or press Enter to browse.'
	elif [[ ! -r $path ]]; then
		draw_preview_message 'UNREADABLE' "$title" 'Read permission is required for a preview.'
	elif is_text_file "$path"; then
		height=$((ROWS - 3)); ((height < 1)) && height=1
		width=$((PREVIEW_W - 3)); ((width < 1)) && width=1
		ln -sfn -- "$path" "$PREVIEW_LINK"
		draw_preview_title "TEXT  $title"
		dcs "open '$PREVIEW_LINK' rect 2 3 $width $height"
	else
		draw_preview_message 'BINARY FILE' "$title" 'No inline preview is available for this type.'
	fi
}

draw_overlay() {
	draw_header
	draw_info
	draw_list
	draw_divider
	draw_status
}

render_all() {
	terminal_size
	if ((COMPACT)); then
		printf '\033[2J\033[H%s click to restore %s' "$HEADER" "$RESET"
		return
	fi
	ensure_visible
	update_info
	printf '\033[2J'
	ATLAS_CLEAN=1
	PREVIEW_WAS_GRAPHIC=0
	render_preview
	draw_overlay
}

reset_click() {
	LAST_CLICK_IDX=-1
	LAST_CLICK_MS=0
}

select_index() {
	local target=$1 old=$IDX old_top=$VIEW_TOP
	((${#FILES[@]})) || return
	((target < 0)) && target=0
	((target >= ${#FILES[@]})) && target=$((${#FILES[@]} - 1))
	((target == IDX)) && return
	IDX=$target
	PAGE=1
	ensure_visible
	update_info
	render_preview
	if ((PREVIEW_FULL_REDRAW)); then
		draw_overlay
	else
		draw_info
		if ((VIEW_TOP != old_top)); then
			draw_list
		else
			draw_list_row "$((old - VIEW_TOP))"
			draw_list_row "$((IDX - VIEW_TOP))"
		fi
		draw_status
	fi
}

move_selection() {
	((${#FILES[@]})) || return
	select_index "$((IDX + $1))"
}

change_directory() {
	local next=$1 wanted=${2:-}
	if ! next=$(cd -- "$next" 2>/dev/null && pwd -P); then
		STATUS='Cannot enter directory'
		draw_status
		return
	fi
	DIR=$next
	reset_click
	IDX=0
	VIEW_TOP=0
	PAGE=1
	build_list
	if [[ -n $wanted ]]; then
		local i
		for i in "${!FILES[@]}"; do
			if [[ ${FILES[$i]} == "$wanted" ]]; then IDX=$i; break; fi
		done
	fi
	display_text "$DIR"
	STATUS="Browsing $REPLY"
	render_all
}

go_parent() {
	[[ $DIR == / ]] && return
	local old=$DIR parent=${DIR%/*}
	[[ -n $parent ]] || parent=/
	change_directory "$parent" "$old"
}

enter_selected() {
	((IDX >= 0)) || return
	local path=${FILES[$IDX]}
	if [[ -d $path ]]; then
		if [[ $path == "$DIR/.." ]]; then go_parent; else change_directory "$path"; fi
	else
		open_selected 0
	fi
}

open_path() {
	local path=$1 minimize=${2:-0} name
	base_name "$path"
	name=$REPLY
	if [[ -z $OPEN_CMD ]]; then
		STATUS='External opening is disabled'
	else
		if ((minimize)); then
			window_dcs place bottom-left 0px 0px 24% 8% restore "$GEOMETRY_TAG"
		fi
		FILE=$path NAME=$name BROWSER_DIR=$DIR \
			bash -c "$OPEN_CMD" </dev/null >/dev/null 2>&1 &
		display_text "$name"
		STATUS="Opened $REPLY"
	fi
	draw_status
}

open_selected() {
	((IDX >= 0)) || return
	local minimize=${1:-0} path=${FILES[$IDX]}
	[[ -d $path ]] && { enter_selected; return; }
	open_path "$path" "$minimize"
}

double_click_selected() {
	((IDX >= 0)) || return
	if [[ -d ${FILES[$IDX]} ]]; then enter_selected; else open_selected 1; fi
}

refresh_list() {
	local selected=
	reset_click
	((IDX >= 0)) && selected=${FILES[$IDX]}
	build_list
	IDX=0
	local i
	for i in "${!FILES[@]}"; do [[ ${FILES[$i]} == "$selected" ]] && { IDX=$i; break; }; done
	STATUS='Refreshed'
	render_all
}

toggle_hidden() {
	if ((SHOW_HIDDEN)); then SHOW_HIDDEN=0; STATUS='Hidden files off'; else SHOW_HIDDEN=1; STATUS='Hidden files on'; fi
	refresh_list
}

change_pdf_page() {
	((IDX >= 0)) || return
	is_pdf "${FILES[$IDX]}" || return
	PAGE=$((PAGE + $1))
	((PAGE < 1)) && PAGE=1
	update_info
	render_preview
	draw_info
	draw_status
}

now_ms() {
	local stamp=${EPOCHREALTIME:-}
	if [[ -n $stamp ]]; then
		stamp=${stamp/./}
		NOW_MS=$((10#$stamp / 1000))
	else
		NOW_MS=$(date +%s%3N)
	fi
}

mouse_event() {
	local cb=$1 x=$2 y=$3 final=$4 button index
	button=$((cb & 3))
	[[ $final == M ]] || return
	if ((cb & 64)); then
		reset_click
		if ((x < LIST_X && IDX >= 0)) && is_pdf "${FILES[$IDX]}"; then
			if ((button == 0)); then change_pdf_page -1; elif ((button == 1)); then change_pdf_page 1; fi
		else
			if ((button == 0)); then move_selection -3; elif ((button == 1)); then move_selection 3; fi
		fi
		return
	fi
	((cb & 32)) && return
	if ((button == 2)); then reset_click; go_parent; return; fi
	((button == 0 && x >= LIST_X && y >= LIST_TOP && y <= LIST_BOTTOM)) || return
	index=$((VIEW_TOP + y - LIST_TOP))
	((index < ${#FILES[@]})) || return
	now_ms
	if ((index == LAST_CLICK_IDX && NOW_MS - LAST_CLICK_MS <= DOUBLE_CLICK_MS)); then
		select_index "$index"
		LAST_CLICK_IDX=-1
		double_click_selected
	else
		select_index "$index"
		LAST_CLICK_IDX=$index
		LAST_CLICK_MS=$NOW_MS
	fi
}

handle_csi() {
	local first=$1 rest= char payload cb x y final
	case $first in
		A) reset_click; move_selection -1 ;;
		B) reset_click; move_selection 1 ;;
		C) reset_click; enter_selected ;;
		D) reset_click; go_parent ;;
		H) reset_click; select_index 0 ;;
		F) reset_click; select_index "$((${#FILES[@]} - 1))" ;;
		'<')
			payload=
			while ((${#payload} < 64)) && IFS= read -rsn1 -t 0.05 char; do
				payload+=$char
				[[ $char == M || $char == m ]] && break
			done
			if [[ $payload =~ ^([0-9]+)\;([0-9]+)\;([0-9]+)([Mm])$ ]]; then
				cb=${BASH_REMATCH[1]}; x=${BASH_REMATCH[2]}; y=${BASH_REMATCH[3]}; final=${BASH_REMATCH[4]}
				mouse_event "$cb" "$x" "$y" "$final"
			fi
			;;
		[0-9])
			reset_click
			rest=$first
			while ((${#rest} < 16)) && IFS= read -rsn1 -t 0.05 char; do
				rest+=$char
				[[ $char == '~' || $char =~ [A-Za-z] ]] && break
			done
			case $rest in
				1~|7~) select_index 0 ;;
				4~|8~) select_index "$((${#FILES[@]} - 1))" ;;
				5~) move_selection "$((-VISIBLE))" ;;
				6~) move_selection "$VISIBLE" ;;
			esac
			;;
	esac
}

activate_typed_path() {
	local path=$1
	if [[ -d $path ]]; then
		PATH_ACTIVE=0
		change_directory "$path"
	else
		PATH_ACTIVE=0
		open_path "$path" 0
		render_all
	fi
}

submit_path_prompt() {
	local exact
	if ((PATH_MATCH_IDX >= 0 && PATH_MATCH_IDX < ${#PATH_MATCHES[@]})); then
		activate_typed_path "${PATH_MATCHES[$PATH_MATCH_IDX]}"
		return
	fi
	path_pattern "$PATH_BUFFER" 0; exact=$REPLY
	if ! path_has_glob "$PATH_BUFFER" && [[ -e $exact || -L $exact ]]; then
		activate_typed_path "$exact"
		return
	fi
	update_path_matches 0
	case ${#PATH_MATCHES[@]} in
		0) PATH_MESSAGE='No match' ;;
		1) activate_typed_path "${PATH_MATCHES[0]}"; return ;;
		*) PATH_MESSAGE="${#PATH_MATCHES[@]} matches; use Up/Down" ;;
	esac
	draw_path_prompt
}

drain_path_mouse() {
	local char payload=
	while ((${#payload} < 64)) && IFS= read -rsn1 -t 0.05 char; do
		payload+=$char
		[[ $char == M || $char == m ]] && break
	done
	PATH_ABORT=1
}

handle_path_csi() {
	local first=$1 char rest
	case $first in
		A) path_select_delta -1 ;;
		B) path_select_delta 1 ;;
		C) ((PATH_CURSOR < ${#PATH_BUFFER})) && PATH_CURSOR=$((PATH_CURSOR + 1)) ;;
		D) ((PATH_CURSOR > 0)) && PATH_CURSOR=$((PATH_CURSOR - 1)) ;;
		H) PATH_CURSOR=0 ;;
		F) PATH_CURSOR=${#PATH_BUFFER} ;;
		'<') drain_path_mouse ;;
		[0-9])
			rest=$first
			while ((${#rest} < 16)) && IFS= read -rsn1 -t 0.05 char; do
				rest+=$char
				[[ $char == '~' || $char =~ [A-Za-z] ]] && break
			done
			case $rest in
				1~|7~) PATH_CURSOR=0 ;;
				3~) path_delete ;;
				4~|8~) PATH_CURSOR=${#PATH_BUFFER} ;;
			esac
			;;
		*) PATH_ABORT=1 ;;
	esac
}

path_prompt() {
	local key next first
	PATH_ACTIVE=1
	PATH_ABORT=0
	PATH_BUFFER=/
	PATH_CURSOR=1
	PATH_MATCH_IDX=-1
	PATH_MESSAGE=
	update_path_matches 1
	draw_path_prompt
	while ((PATH_ACTIVE && !PATH_ABORT)); do
		if ((RESIZED)); then
			RESIZED=0
			render_all
			((COMPACT)) && { PATH_ABORT=1; break; }
			draw_path_prompt
		fi
		if ! IFS= read -rsn1 -t 0.1 key; then
			((PATH_ABORT)) && break
			((RESIZED)) && continue
			[[ -t 0 ]] && continue
			PATH_ACTIVE=0
			RUNNING=0
			break
		fi
		case $key in
			''|$'\r'|$'\n') submit_path_prompt ;;
			$'\177'|$'\b') path_backspace ;;
			$'\001') PATH_CURSOR=0 ;;
			$'\005') PATH_CURSOR=${#PATH_BUFFER} ;;
			$'\003') PATH_ABORT=1 ;;
			$'\e')
				if IFS= read -rsn1 -t 0.05 next; then
					if [[ $next == '[' ]] && IFS= read -rsn1 -t 0.05 first; then handle_path_csi "$first"; else PATH_ABORT=1; fi
				else PATH_ABORT=1
				fi
				;;
			$'\000'|$'\002'|$'\004'|$'\006'|$'\007'|$'\013'|$'\014'|$'\016'|$'\017'|$'\020'|$'\021'|$'\022'|$'\023'|$'\024'|$'\025'|$'\026'|$'\027'|$'\030'|$'\031'|$'\032'|$'\034'|$'\035'|$'\036'|$'\037') ;;
			*) path_insert "$key" ;;
		esac
		((PATH_ACTIVE && !PATH_ABORT)) && draw_path_prompt
	done
	if ((PATH_ABORT)); then
		PATH_ACTIVE=0
		STATUS='Path entry cancelled'
		render_all
	fi
}

terminal_size
build_list
printf '\033[?1049h\033[?25l\033[?1000h\033[?1006h'
window_dcs remember "$GEOMETRY_TAG"
render_all

RUNNING=1
while ((RUNNING)); do
	if ((RESIZED)); then RESIZED=0; STATUS='Resized'; render_all; fi
	if ! IFS= read -rsn1 key; then
		((RESIZED)) && continue
		break
	fi
	case $key in
		q|Q) RUNNING=0 ;;
		'/') reset_click; path_prompt ;;
		'.') reset_click; toggle_hidden ;;
		r|R) reset_click; refresh_list ;;
		o|O) reset_click; open_selected 0 ;;
		'[') reset_click; change_pdf_page -1 ;;
		']') reset_click; change_pdf_page 1 ;;
		$'\r'|$'\n') reset_click; enter_selected ;;
		$'\177'|$'\b') reset_click; go_parent ;;
		$'\e')
			if IFS= read -rsn1 -t 0.05 next; then
				if [[ $next == '[' ]] && IFS= read -rsn1 -t 0.05 first; then handle_csi "$first"; fi
			else
				RUNNING=0
			fi
			;;
	esac
done
