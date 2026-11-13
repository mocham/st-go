# st-go: a Golang clone of suckless st.
#
# Third-party static libraries (all downloaded & built into third_party/ on
# demand; the whole folder is gitignored and reproduced on demand):
#   - FreeType2   : glyph rasterization
#   - libwebp     : accelerated static/animated WebP decode
#   - poppler     : PDF rendering (first page / page N via "open")
#   - zlib        : poppler's stream decompression
#   - libpng      : poppler's embedded-image support
#   - libjpeg-turbo: poppler's JPEG/DCT image decoder
#   - DjVuLibre  : static DjVu decoding for zat-djvu
#   - FFmpeg/x264: H.264/MP4 recording in st-fb
# SQLite uses the pure-Go modernc.org/sqlite driver on every backend.
#
# Full terminal profiles also use libpng and libjpeg-turbo for accelerated PNG
# and JPEG decoding. Reduced profiles use native Go image codecs. Poppler uses
# the C++/Splash renderer with the same static freetype, zlib, libpng and JPEG
# archives. The JPEG decoder uses libjpeg.a, not the TurboJPEG API.
# GUI bindings, Cairo, color management and JPEG 2000 are disabled.
#
# X is driven by the pure-Go xgb library over sockets, so no X11 C library
# is needed. The result is a fully static binary with no dynamic deps.

VERSION     = 0.9.2
.DEFAULT_GOAL := all
FT_VERSION  = 2.13.3
FT_URL      = https://download-mirror.savannah.gnu.org/releases/freetype/freetype-$(FT_VERSION).tar.xz

FT_DIR      = third_party/freetype
FT_SRC      = third_party/freetype-$(FT_VERSION)
FT_TARBALL  = third_party/freetype-$(FT_VERSION).tar.xz
FT_A        = $(FT_DIR)/libfreetype.a

OUTPUT_DIR  = BINARIES
BIN         = $(OUTPUT_DIR)/st
DJVU_SYSTEM_AVAILABLE := $(shell pkg-config --exists ddjvuapi 2>/dev/null && echo 1)
BINARY_NAMES = st st-min st-web st-fb st-fb-min file-browser gols zat zat-djvu goiv govim opencode-tui
BINARY_PATHS = $(addprefix $(OUTPUT_DIR)/,$(BINARY_NAMES))
FUNCTIONAL_MODULES = $(addprefix functionalities/,audio config googlepinyin latexpdf limits pdfmeta ptyutil semhtml terminfo tui widgets)
SHARED_MODULES = $(FUNCTIONAL_MODULES) platform_neutral/api platform_neutral/stterminal term
# Standalone, asset-free module tests use the system API when available. The
# distribution target below never does: it always uses the pinned static build.
DEMO_MODULES = demo-go demo-go/file-browser demo-go/goiv demo-go/gols demo-go/govim demo-go/opencode-tui demo-go/zat $(if $(DJVU_SYSTEM_AVAILABLE),demo-go/zat-djvu)
GO_MODULES = $(SHARED_MODULES) $(DEMO_MODULES)
MODULE_META = $(wildcard go.work go.work.sum) $(foreach dir,. $(GO_MODULES),$(wildcard $(dir)/go.mod $(dir)/go.sum))
CFG         = functionalities/config/config.json
PREFIX     ?= /usr/local
BINDIR     ?= $(PREFIX)/bin
TERMINFO_DIR ?= $(PREFIX)/share/terminfo

# The decoded-WebP SQLite cache is only used by the full native terminal.
TABS ?= 1
TAB_GO_TAGS = $(if $(filter 1,$(TABS)),terminal_tabs,)
FULL_GO_TAGS = fastimage libwebp webpcache $(TAB_GO_TAGS)
TERMINAL_NAMES = st st-web st-fb st-min st-fb-min
TERMINAL_PROFILE = $(OUTPUT_DIR)/.terminal-profile

# Sources that affect the Go file-browser demo, including the sibling
# latexpdf module and semantic-HTML renderer. Audio is terminal-owned.
FILE_BROWSER = $(OUTPUT_DIR)/file-browser
LATEXPDF_SRC = $(wildcard functionalities/latexpdf/*.go) \
	       $(wildcard functionalities/latexpdf/pdf_assets/*)
SEMHTML_SRC = $(wildcard functionalities/semhtml/*.go)
TUI_NAV_SRC = $(wildcard functionalities/tui/navigation/*.go)
TUI_SEARCH_SRC = $(wildcard functionalities/tui/textsearch/*.go)
TUI_READLINE_SRC = $(wildcard functionalities/tui/readline/*.go functionalities/tui/pinyin/*.go functionalities/googlepinyin/*.go functionalities/googlepinyin/assets/*)
STTERMINAL_SRC = $(wildcard term/*.go platform_neutral/stterminal/*.go)
FB_GO_SRC    = $(wildcard demo-go/file-browser/*.go) \
	       $(wildcard platform_neutral/api/*.go) \
	       $(wildcard demo-go/file-browser/*.conf functionalities/tui/audio/*.go functionalities/tui/tabs/*.go functionalities/tui/preview/*.go) go.mod go.sum $(LATEXPDF_SRC) $(SEMHTML_SRC) $(TUI_NAV_SRC) $(TUI_READLINE_SRC)
GOLS_GO_SRC  = $(wildcard demo-go/gols/*.go) go.mod go.sum $(LATEXPDF_SRC) $(TUI_NAV_SRC) $(TUI_SEARCH_SRC) $(TUI_READLINE_SRC) $(wildcard functionalities/pdfmeta/*.go functionalities/limits/*.go)
ZAT_GO_SRC = $(wildcard demo-go/zat/*.go functionalities/limits/*.go functionalities/pdfmeta/*.go) \
	       $(STTERMINAL_SRC) $(wildcard functionalities/config/*.go functionalities/config/*.json) $(TUI_NAV_SRC) $(TUI_SEARCH_SRC) $(TUI_READLINE_SRC) go.mod go.sum
ZAT_DJVU_SRC = $(wildcard demo-go/zat-djvu/*.go demo-go/zat-djvu/*.c demo-go/zat-djvu/*.h demo-go/zat-djvu/go.mod demo-go/zat-djvu/go.sum functionalities/limits/*.go functionalities/pdfmeta/*.go) \
	       $(STTERMINAL_SRC) $(wildcard functionalities/config/*.go functionalities/config/*.json) $(TUI_NAV_SRC) $(TUI_SEARCH_SRC) $(TUI_READLINE_SRC) go.mod go.sum
GOIV_GO_SRC = $(wildcard demo-go/goiv/*.go) go.mod go.sum
TUI_WRITING_SRC = $(wildcard functionalities/tui/appbus/*.go functionalities/tui/writing/*.go)
GOVIM_GO_SRC = $(wildcard demo-go/govim/*.go demo-go/govim/go.* demo-go/govim/syntax/* demo-go/govim/imap/* functionalities/tui/imap/*.go functionalities/tui/preview/*.go functionalities/tui/outline/*.go functionalities/config/*.go functionalities/config/*.json) $(STTERMINAL_SRC) $(TUI_READLINE_SRC) $(TUI_WRITING_SRC) $(LATEXPDF_SRC) $(wildcard functionalities/limits/*.go) go.mod go.sum
OPENCODE_TUI_GO_SRC = $(wildcard demo-go/opencode-tui/*.go demo-go/opencode-tui/go.* functionalities/tui/preview/*.go platform_neutral/api/*.go) $(TUI_READLINE_SRC) $(TUI_WRITING_SRC) $(LATEXPDF_SRC) $(wildcard functionalities/limits/*.go) go.mod go.sum

# --- poppler (PDF rendering) ---------------------------------------------
# Static C++/Splash rendering, including embedded JPEG images through libjpeg.
POPPLER_VERSION = 25.07.0
POPPLER_URL     = https://poppler.freedesktop.org/poppler-$(POPPLER_VERSION).tar.xz
POPPLER_TAR     = third_party/poppler-$(POPPLER_VERSION).tar.xz
POPPLER_SRC     = third_party/poppler-src
POPPLER_BUILD   = third_party/poppler-src/build
POPPLER_LIB     = third_party/poppler/lib/libpoppler.a

ZLIB_VERSION = 1.3.1
ZLIB_URL     = https://zlib.net/fossils/zlib-$(ZLIB_VERSION).tar.gz
ZLIB_TAR     = third_party/zlib-$(ZLIB_VERSION).tar.gz
ZLIB_SRC     = third_party/zlib-src
ZLIB_LIB     = third_party/poppler/lib/libz.a

PNG_VERSION = 1.6.44
PNG_URL     = https://download.sourceforge.net/libpng/libpng-$(PNG_VERSION).tar.gz
PNG_TAR     = third_party/libpng-$(PNG_VERSION).tar.gz
PNG_SRC     = third_party/libpng-src
PNG_LIB     = third_party/poppler/lib/libpng16.a

JPEG_VERSION = 3.2.1
JPEG_URL     = https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/$(JPEG_VERSION)/libjpeg-turbo-$(JPEG_VERSION).tar.gz
JPEG_SHA256  = c27ad72a68da151177929aa969ae773a2da5ac2af9ce946079314fdac8cece7f
JPEG_TAR     = third_party/libjpeg-turbo-$(JPEG_VERSION).tar.gz
JPEG_SRC     = third_party/libjpeg-turbo-src
JPEG_BUILD   = $(JPEG_SRC)/build
JPEG_DIR     = third_party/jpeg
JPEG_LIB     = $(JPEG_DIR)/lib/libjpeg.a

DJVU_VERSION = 3.5.29
DJVU_URL     = https://downloads.sourceforge.net/project/djvu/DjVuLibre/$(DJVU_VERSION)/djvulibre-$(DJVU_VERSION).tar.gz
DJVU_SHA256  = d3b4b03ae2bdca8516a36ef6eb27b777f0528c9eda26745d9962824a3fdfeccf
DJVU_TAR     = third_party/djvulibre-$(DJVU_VERSION).tar.gz
DJVU_SRC     = third_party/djvulibre-src
DJVU_DIR     = third_party/djvu
DJVU_LIB     = $(DJVU_DIR)/lib/libdjvulibre.a
DJVU_STAMP   = $(DJVU_DIR)/include/st_go_djvu_static.h

# libwebp: accelerated static and animated WebP decode for full profiles.
WEBP_VERSION = 1.6.0
WEBP_URL     = https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-$(WEBP_VERSION).tar.gz
WEBP_TAR     = third_party/libwebp-$(WEBP_VERSION).tar.gz
WEBP_SRC     = third_party/libwebp-src
WEBP_LIB     = third_party/webp/lib/libwebp.a
WEBP_DEMUX   = third_party/webp/lib/libwebpdemux.a
WEBP_ANIM_C  = third_party_wrapper/webp_anim.c
WEBP_ANIM_O  = third_party/webp/webp_anim.o

# Framebuffer recording: a deliberately small, static FFmpeg + x264 build.
X264_VERSION = b35605ace3ddf7c1a5d67a2eb553f034aef41d55
X264_URL = https://code.videolan.org/videolan/x264/-/archive/$(X264_VERSION)/x264-$(X264_VERSION).tar.bz2
X264_SHA256 = 6eeb82934e69fd51e043bd8c5b0d152839638d1ce7aa4eea65a3fedcf83ff224
X264_TAR = third_party/x264-$(X264_VERSION).tar.bz2
X264_SRC = third_party/x264-src
X264_DIR = third_party/x264
X264_LIB = $(X264_DIR)/lib/libx264.a
FFMPEG_VERSION = 8.1.3
FFMPEG_URL = https://ffmpeg.org/releases/ffmpeg-$(FFMPEG_VERSION).tar.xz
FFMPEG_SHA256 = 7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3
FFMPEG_TAR = third_party/ffmpeg-$(FFMPEG_VERSION).tar.xz
FFMPEG_SRC = third_party/ffmpeg-src
FFMPEG_DIR = third_party/ffmpeg
FFMPEG_LIBS = $(addprefix $(FFMPEG_DIR)/lib/,libavformat.a libavcodec.a libswscale.a libavutil.a)
FFMPEG_BRIDGE = third_party/recording_ffmpeg.o
FFMPEG_STAMP = $(FFMPEG_DIR)/include/st_go_ffmpeg_static.h

# Build every terminal profile/backend, demo, and helper.
all: $(BINARY_PATHS)

# --- FreeType2 static archive ------------------------------------------
# Fetched only when missing. Configured WITHOUT zlib/libpng/brotli/bzip2 so
# the static archive pulls in no compression/image libraries at all.
$(FT_A):
	@mkdir -p third_party
	@if [ ! -f "$(FT_TARBALL)" ]; then \
		echo "downloading FreeType $(FT_VERSION)..."; \
		curl -fL -o "$(FT_TARBALL)" "$(FT_URL)"; \
	fi
	rm -rf "$(FT_SRC)" "$(FT_DIR)"
	mkdir -p "$(FT_SRC)"
	tar -xJf "$(FT_TARBALL)" -C "$(FT_SRC)" --strip-components=1
	cd "$(FT_SRC)" && ./configure --disable-shared --enable-static \
		--without-harfbuzz --without-zlib --without-png \
		--without-brotli --without-bzip2 \
		CFLAGS="-O2 -fPIC -ffunction-sections -fdata-sections"
	$(MAKE) -C "$(FT_SRC)"
	mkdir -p "$(FT_DIR)"
	cp "$(FT_SRC)/objs/.libs/libfreetype.a" "$(FT_A)"
	cp -r "$(FT_SRC)/include" "$(FT_DIR)/include"

# --- zlib static (poppler dep) ------------------------------------------
$(ZLIB_LIB):
	@mkdir -p third_party
	@if [ ! -f "$(ZLIB_TAR)" ]; then \
		echo "downloading zlib $(ZLIB_VERSION)..."; \
		curl -fL -o "$(ZLIB_TAR)" "$(ZLIB_URL)"; \
	fi
	rm -rf "$(ZLIB_SRC)" "$(dir $(ZLIB_LIB))"
	mkdir -p "$(dir $(ZLIB_LIB))" third_party/poppler/include
	mkdir -p "$(ZLIB_SRC)"
	tar -xzf "$(ZLIB_TAR)" -C "$(ZLIB_SRC)" --strip-components=1
	cd "$(ZLIB_SRC)" && CFLAGS="-O2 -fPIC -ffunction-sections -fdata-sections" ./configure --static --prefix=$$PWD/out
	$(MAKE) -C "$(ZLIB_SRC)"
	cp "$(ZLIB_SRC)/libz.a" "$(ZLIB_LIB)"
	cp "$(ZLIB_SRC)/zlib.h" "$(ZLIB_SRC)/zconf.h" third_party/poppler/include/

# --- libpng static (full raster fast path and poppler dependency) ---------
$(PNG_LIB): $(ZLIB_LIB)
	@mkdir -p third_party
	@if [ ! -f "$(PNG_TAR)" ]; then \
		echo "downloading libpng $(PNG_VERSION)..."; \
		curl -fL -o "$(PNG_TAR)" "$(PNG_URL)"; \
	fi
	rm -rf "$(PNG_SRC)"
	mkdir -p "$(dir $(PNG_LIB))" third_party/poppler/include
	mkdir -p "$(PNG_SRC)"
	tar -xzf "$(PNG_TAR)" -C "$(PNG_SRC)" --strip-components=1
	cd "$(PNG_SRC)" && ./configure --disable-shared --enable-static CFLAGS="-fPIC -O2 -ffunction-sections -fdata-sections" LDFLAGS="-L$$PWD/../poppler/lib"
	$(MAKE) -C "$(PNG_SRC)"
	cp "$(PNG_SRC)/.libs/libpng16.a" "$(PNG_LIB)"
	cp "$(PNG_SRC)/png.h" "$(PNG_SRC)/pngconf.h" "$(PNG_SRC)/pnglibconf.h" third_party/poppler/include/

# --- libjpeg-turbo static (full raster fast path and poppler DCTDecode) ----
# Build only the libjpeg API. SIMD is auto-detected; NASM enables it on x86.
# Archive targets deliberately do not depend on the whole Makefile: changing
# app targets or test commands must not rebuild native dependencies. Explicitly
# force the affected archive target when changing its version/build settings.
$(JPEG_LIB):
	@mkdir -p third_party
	@if [ ! -f "$(JPEG_TAR)" ]; then \
		echo "downloading libjpeg-turbo $(JPEG_VERSION)..."; \
		curl -fL --retry 3 -o "$(JPEG_TAR).tmp" "$(JPEG_URL)" && \
		mv "$(JPEG_TAR).tmp" "$(JPEG_TAR)"; \
	fi
	printf '%s  %s\n' '$(JPEG_SHA256)' '$(JPEG_TAR)' | sha256sum -c -
	rm -rf "$(JPEG_SRC)" "$(JPEG_DIR)"
	mkdir -p "$(JPEG_SRC)" "$(JPEG_DIR)/lib" "$(JPEG_DIR)/include"
	tar -xzf "$(JPEG_TAR)" -C "$(JPEG_SRC)" --strip-components=1
	cmake -S "$(JPEG_SRC)" -B "$(JPEG_BUILD)" \
		-DCMAKE_BUILD_TYPE=Release -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
		-DCMAKE_C_FLAGS="-ffunction-sections -fdata-sections" \
		-DENABLE_SHARED=OFF -DENABLE_STATIC=ON -DWITH_TURBOJPEG=OFF \
		-DWITH_TOOLS=OFF -DWITH_TESTS=OFF
	$(MAKE) -C "$(JPEG_BUILD)" jpeg-static
	cp "$(JPEG_BUILD)/libjpeg.a" "$(JPEG_LIB)"
	cp "$(JPEG_BUILD)/jconfig.h" "$(JPEG_SRC)/src/jpeglib.h" \
		"$(JPEG_SRC)/src/jmorecfg.h" "$(JPEG_SRC)/src/jerror.h" "$(JPEG_DIR)/include/"

# --- DjVuLibre static (zat-djvu page/text/outline decoder) ----------------
$(DJVU_TAR):
	@mkdir -p third_party
	curl --fail --location --retry 3 --output "$@.tmp" "$(DJVU_URL)"
	printf '%s  %s\n' '$(DJVU_SHA256)' '$@.tmp' | sha256sum -c -
	mv "$@.tmp" "$@"

$(DJVU_LIB): $(JPEG_LIB)
	@mkdir -p third_party
	@if [ ! -f "$(DJVU_TAR)" ]; then \
		$(MAKE) --no-print-directory "$(DJVU_TAR)"; \
	fi
	printf '%s  %s\n' '$(DJVU_SHA256)' '$(DJVU_TAR)' | sha256sum -c -
	rm -rf "$(DJVU_SRC)" "$(DJVU_DIR)"
	mkdir -p "$(DJVU_SRC)" "$(DJVU_DIR)/lib" "$(DJVU_DIR)/include/libdjvu"
	tar -xzf "$(DJVU_TAR)" -C "$(DJVU_SRC)" --strip-components=1
	# Static glibc cannot service NSS-backed passwd lookups without matching
	# shared objects. zat-djvu supplies absolute paths and HOME, so disable only
	# DjVuLibre's unused ~user/passwd fallbacks, like the static ALSA profile.
	sed -i 's/pw=getpwnam(user);/pw=0;/' "$(DJVU_SRC)/libdjvu/GURL.cpp"
	sed -i 's/pw = getpwnam((const char \*)senv.getUTF82Native());/pw = 0;/' "$(DJVU_SRC)/libdjvu/GURL.cpp"
	sed -i 's/pw=getpwuid(getuid());/pw=0;/' "$(DJVU_SRC)/libdjvu/GURL.cpp"
	sed -i 's/if ((pw = getpwuid(getuid())))/if ((pw = 0))/' "$(DJVU_SRC)/libdjvu/DjVuMessage.cpp"
	sed -i 's/passwd \*pswd = getpwuid(getuid());/passwd *pswd = 0;/' "$(DJVU_SRC)/libdjvu/DjVuToPS.cpp"
	cd "$(DJVU_SRC)" && ./configure \
		--prefix="$(abspath $(DJVU_DIR))" \
		--disable-shared --enable-static --disable-desktopfiles \
		--disable-xmltools --without-tiff --with-jpeg="$(abspath $(JPEG_DIR))" \
		CFLAGS="-O2 -fPIC -ffunction-sections -fdata-sections" \
		CXXFLAGS="-O2 -fPIC -ffunction-sections -fdata-sections"
	$(MAKE) -C "$(DJVU_SRC)/libdjvu"
	cp "$(DJVU_SRC)/libdjvu/.libs/libdjvulibre.a" "$(DJVU_LIB)"
	cp "$(DJVU_SRC)/libdjvu/ddjvuapi.h" "$(DJVU_SRC)/libdjvu/miniexp.h" "$(DJVU_DIR)/include/libdjvu/"
	cp "$(DJVU_SRC)/COPYING" "$(DJVU_DIR)/LICENSE"

# Go's cgo cache hashes included headers but not external archive contents.
# Embed the archive identity in a generated header so rebuilt native code can
# never be hidden by a stale cached cgo package.
$(DJVU_STAMP): $(DJVU_LIB)
	@mkdir -p "$(@D)"
	@hash=$$(sha256sum "$(DJVU_LIB)" | cut -d' ' -f1); \
		printf '#define ST_GO_DJVU_STATIC_SHA256 "%s"\n' "$$hash" > "$@.tmp"; \
		mv "$@.tmp" "$@"

# --- libwebp static (accelerated static/animated WebP decode) --------------
$(WEBP_LIB):
	@mkdir -p third_party
	@if [ ! -f "$(WEBP_TAR)" ]; then \
		echo "downloading libwebp $(WEBP_VERSION)..."; \
		curl -fL -o "$(WEBP_TAR)" "$(WEBP_URL)"; \
	fi
	rm -rf "$(WEBP_SRC)" third_party/webp
	mkdir -p "$(WEBP_SRC)" third_party/webp/lib third_party/webp/include/webp
	tar -xzf "$(WEBP_TAR)" -C "$(WEBP_SRC)" --strip-components=1
	cd "$(WEBP_SRC)" && ./configure CFLAGS="-fPIC -O2 -ffunction-sections -fdata-sections" --enable-static --disable-shared
	$(MAKE) -C "$(WEBP_SRC)"
	# the webp tarball ships a swig/libwebp.go which would make "go test ./..."
	# try to build it as a Go package; drop it (and other non-C build dirs).
	rm -rf "$(WEBP_SRC)/swig" "$(WEBP_SRC)/man" "$(WEBP_SRC)/extras"
	cp "$(WEBP_SRC)/src/.libs/libwebp.a" "$(WEBP_LIB)"

# The animation decoder (WebPAnimDecoder) lives in libwebpdemux; the terminal
# uses it to play animated webp files (open DSL `anim` option).
$(WEBP_DEMUX): $(WEBP_LIB)
	cp "$(WEBP_SRC)/src/demux/.libs/libwebpdemux.a" "$@"
	cp "$(WEBP_SRC)/src/webp/"*.h third_party/webp/include/webp/

# Animated-WebP bridge (WebPAnimDecoder) for the Go file-browser, precompiled
# so the cgo file only declares extern functions.
$(WEBP_ANIM_O): $(WEBP_ANIM_C) $(WEBP_LIB) $(WEBP_DEMUX)
	@mkdir -p "$(dir $@)"
	$(CC) -O2 -fPIC -Ithird_party/webp/include -c "$<" -o "$@"

# --- poppler static (minimal, C++ API only) ------------------------------
$(POPPLER_LIB): $(FT_A) $(ZLIB_LIB) $(PNG_LIB) $(JPEG_LIB)
	@mkdir -p third_party
	@if [ ! -f "$(POPPLER_TAR)" ]; then \
		echo "downloading poppler $(POPPLER_VERSION)..."; \
		curl -fL -o "$(POPPLER_TAR)" "$(POPPLER_URL)"; \
	fi
	rm -rf "$(POPPLER_SRC)" "$(POPPLER_BUILD)"
	mkdir -p "$(POPPLER_SRC)" third_party/poppler/include
	tar -xJf "$(POPPLER_TAR)" -C "$(POPPLER_SRC)" --strip-components=1
	mkdir -p "$(POPPLER_BUILD)"
	cd "$(POPPLER_BUILD)" && cmake .. \
		-DCMAKE_BUILD_TYPE=Release \
		-DCMAKE_CXX_FLAGS="-fPIC -ffunction-sections -fdata-sections" \
		-DCMAKE_C_FLAGS="-fPIC -ffunction-sections -fdata-sections" \
		-DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
		-DFREETYPE_INCLUDE_DIRS="$(abspath third_party/freetype/include)" \
		-DFREETYPE_LIBRARY="$(abspath third_party/freetype/libfreetype.a)" \
		-DZLIB_INCLUDE_DIR="$(abspath third_party/poppler/include)" \
		-DZLIB_LIBRARY="$(abspath third_party/poppler/lib/libz.a)" \
		-DPNG_INCLUDE_DIR="$(abspath third_party/poppler/include)" \
		-DPNG_LIBRARY="$(abspath third_party/poppler/lib/libpng16.a)" \
		-DJPEG_INCLUDE_DIR="$(abspath $(JPEG_DIR)/include)" \
		-DJPEG_LIBRARY="$(abspath $(JPEG_LIB))" \
		-DFONT_CONFIGURATION=generic \
		-DENABLE_GLIB=OFF -DENABLE_QT5=OFF -DENABLE_QT6=OFF \
		-DENABLE_BOOST=OFF -DENABLE_CAIRO=OFF -DENABLE_LCMS=OFF \
		-DENABLE_LIBCURL=OFF -DENABLE_LIBTIFF=OFF -DENABLE_NSS3=OFF \
		-DENABLE_GPGME=OFF -DENABLE_LIBOPENJPEG=none -DENABLE_DCTDECODER=libjpeg \
		-DENABLE_CPP=ON -DENABLE_UTILS=OFF -DENABLE_GTK_DOC=OFF \
		-DBUILD_QT5_TESTS=OFF -DBUILD_QT6_TESTS=OFF -DBUILD_CPP_TESTS=OFF
	$(MAKE) -C "$(POPPLER_BUILD)"
	cp "$(POPPLER_BUILD)/libpoppler.a" "$(POPPLER_BUILD)/cpp/libpoppler-cpp.a" third_party/poppler/lib/
	cp "$(POPPLER_SRC)/cpp/"*.h third_party/poppler/include/
	cp "$(POPPLER_BUILD)/cpp/poppler_cpp_export.h" third_party/poppler/include/

# --- static framebuffer recording ----------------------------------------
$(X264_TAR):
	@mkdir -p third_party
	curl --fail --location --retry 3 --output "$@.tmp" "$(X264_URL)"
	printf '%s  %s\n' '$(X264_SHA256)' '$@.tmp' | sha256sum -c -
	mv "$@.tmp" "$@"

$(X264_LIB):
	@if [ ! -f "$(X264_TAR)" ]; then $(MAKE) --no-print-directory "$(X264_TAR)"; fi
	printf '%s  %s\n' '$(X264_SHA256)' '$(X264_TAR)' | sha256sum -c -
	rm -rf "$(X264_SRC)" "$(X264_DIR)"
	mkdir -p "$(X264_SRC)"
	tar -xjf "$(X264_TAR)" -C "$(X264_SRC)" --strip-components=1
	cd "$(X264_SRC)" && ./configure --prefix="$(abspath $(X264_DIR))" \
		--enable-static --enable-pic --disable-cli --disable-opencl \
		--bit-depth=8 --chroma-format=420 \
		--extra-cflags="-O2 -ffunction-sections -fdata-sections"
	$(MAKE) -C "$(X264_SRC)" install-lib-static
	cp "$(X264_SRC)/COPYING" "$(X264_DIR)/LICENSE"

$(FFMPEG_TAR):
	@mkdir -p third_party
	curl --fail --location --retry 3 --output "$@.tmp" "$(FFMPEG_URL)"
	printf '%s  %s\n' '$(FFMPEG_SHA256)' '$@.tmp' | sha256sum -c -
	mv "$@.tmp" "$@"

# One recipe owns all four archives, including under parallel make. No host
# media libraries are auto-detected; only local-file H.264/MP4 recording is built.
$(FFMPEG_LIBS) &: $(X264_LIB)
	@if [ ! -f "$(FFMPEG_TAR)" ]; then $(MAKE) --no-print-directory "$(FFMPEG_TAR)"; fi
	printf '%s  %s\n' '$(FFMPEG_SHA256)' '$(FFMPEG_TAR)' | sha256sum -c -
	rm -rf "$(FFMPEG_SRC)" "$(FFMPEG_DIR)"
	mkdir -p "$(FFMPEG_SRC)"
	tar -xJf "$(FFMPEG_TAR)" -C "$(FFMPEG_SRC)" --strip-components=1
	cd "$(FFMPEG_SRC)" && PKG_CONFIG_PATH="$(abspath $(X264_DIR)/lib/pkgconfig)" \
		./configure --prefix="$(abspath $(FFMPEG_DIR))" --pkg-config-flags=--static \
		--enable-static --disable-shared --enable-pic --enable-gpl --enable-libx264 \
		--disable-autodetect --disable-everything --disable-programs --disable-doc \
		--disable-debug --disable-network --disable-iconv --disable-avdevice --disable-avfilter --disable-swresample \
		--enable-avformat --enable-avcodec --enable-avutil --enable-swscale \
		--enable-encoder=libx264 --enable-muxer=mp4 --enable-protocol=file \
		--extra-cflags="-O2 -ffunction-sections -fdata-sections"
	$(MAKE) -C "$(FFMPEG_SRC)" install-libs install-headers
	cp "$(FFMPEG_SRC)/COPYING.GPLv2" "$(FFMPEG_DIR)/LICENSE"

$(FFMPEG_BRIDGE): third_party_wrapper/recording_ffmpeg.c third_party_wrapper/recording_ffmpeg.h $(FFMPEG_LIBS)
	$(CC) -O2 -fPIC -ffunction-sections -fdata-sections -I$(FFMPEG_DIR)/include -c $< -o $@

# cgo must observe rebuilt archive/bridge contents instead of reusing a stale
# Go package: external .a/.o files are not part of Go's own cache key.
$(FFMPEG_STAMP): $(FFMPEG_BRIDGE) $(FFMPEG_LIBS) $(X264_LIB)
	@hash=$$(cat $(FFMPEG_BRIDGE) $(FFMPEG_LIBS) $(X264_LIB) | sha256sum | cut -d' ' -f1); \
		printf '#define ST_GO_FFMPEG_STATIC_SHA256 "%s"\n' "$$hash" > "$@.tmp"; \
		mv "$@.tmp" "$@"

# --- st targets ------------------------------------------------------------
# Full and minimal native profiles for X11 and Linux framebuffer. Every profile
# decodes ordinary raster images with Go; full profiles prefer libpng,
# libjpeg-turbo and libwebp where available:
#   st, st-fb         : freetype + libpng/jpeg + libwebp + poppler
#   st-min, st-fb-min : Go FreeType port and Go raster codecs (CGO-free)
#
# Minimal profiles use Go-only PDF/animation stubs. They need no C compiler,
# native library archives, or external linker, including atlas regeneration.

PDF_BRIDGE_O = third_party/pdf_bridge.o
IMAGE_DECODE_O = third_party/image_decode.o

# extra link items appended to -extldflags (order matters for static libs;
# pdf_bridge.o must precede -lpoppler-cpp so --gc-sections keeps the needed
# poppler objects; --gc-sections relies on the libs being built with
# -ffunction-sections -fdata-sections).
FULL_EXTRA = $(IMAGE_DECODE_O) $(PDF_BRIDGE_O) -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler $(JPEG_LIB) -Lthird_party/poppler/lib -lpng16 -lz -Lthird_party/freetype -lfreetype -lstdc++ -lm $(WEBP_ANIM_O) -Lthird_party/webp/lib -l:libwebpdemux.a $(WEBP_LIB)

# Animated-WebP items for the Go file-browser (WebPAnimDecoder playback).
# webp_anim.o must precede -l:libwebpdemux.a; libwebp.a comes via WEBP_LIB.
WEBP_ANIM_EXTRA = $(WEBP_ANIM_O) -Lthird_party/webp/lib -l:libwebpdemux.a -l:libwebp.a

# --gc-sections drops unreferenced functions from function-sections objects.
GC_EXTRA   = -Wl,--gc-sections

WEB_ASSETS := $(wildcard platform_dependent/web/assets/* platform_dependent/web/assets/pdfjs/* platform_dependent/web/assets/pdfjs/*/*)
PLATFORM_SRC := $(wildcard platform_neutral/*/*.go platform_dependent/*/*.go) $(WEB_ASSETS)
GO_SRC := $(wildcard *.go) $(wildcard term/*.go) $(wildcard functionalities/config/*.go functionalities/audio/*.go functionalities/widgets/*.go) $(PLATFORM_SRC) \
	  go.mod go.sum Monaco_Linux.ttf \
	  $(wildcard functionalities/pdfmeta/*.go functionalities/limits/*.go) \
          $(wildcard functionalities/config/*.json functionalities/ptyutil/*.go functionalities/terminfo/*.go) \
          $(wildcard third_party_wrapper/*.c) $(wildcard third_party_wrapper/*.h) \
          $(wildcard third_party_wrapper/*.cpp) $(wildcard *.h)

# Rebuild when build flags change; trim source and module-cache paths from all
# distributable Go binaries, including their stack-trace/debug metadata.
$(BINARY_PATHS): Makefile $(MODULE_META) | $(OUTPUT_DIR)

# A profile switch must rebuild existing binaries in either direction without
# forcing downloads or rebuilding native libraries.
$(TERMINAL_PROFILE): terminal-profile-force | $(OUTPUT_DIR)
	@if [ "$$(cat "$@" 2>/dev/null)" != "$(FULL_GO_TAGS)" ]; then printf '%s' '$(FULL_GO_TAGS)' > "$@"; fi

$(addprefix $(OUTPUT_DIR)/,$(TERMINAL_NAMES)): $(TERMINAL_PROFILE)

terminal-profile-force:

$(OUTPUT_DIR):
	mkdir -p $@

# Keep familiar make target names, but never create executables in the root.
$(BINARY_NAMES): %: $(OUTPUT_DIR)/%

$(OUTPUT_DIR)/st: $(FT_A) $(POPPLER_LIB) $(WEBP_LIB) $(WEBP_DEMUX) $(WEBP_ANIM_O) $(PDF_BRIDGE_O) $(IMAGE_DECODE_O) $(GO_SRC)
	go build -trimpath -tags '$(FULL_GO_TAGS)' -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(FULL_EXTRA)"' .

# CGO-free server; browser Canvas, image decoding and bundled PDF.js presentation.
$(OUTPUT_DIR)/st-web: $(GO_SRC)
	CGO_ENABLED=0 go build -trimpath -tags 'backend_web netgo osusergo $(TAB_GO_TAGS)' -o $@ .

# Linux console fbdev/evdev host, with the existing full software media renderer.
FB_STATUS_FONT = platform_dependent/fb/system_bar_font_generated.go
FONT_RASTER_SRC = $(filter-out %_test.go,$(wildcard platform_neutral/fontraster/*.go functionalities/limits/*.go))
$(FB_STATUS_FONT): Monaco_Linux.ttf platform_dependent/fb/generate_system_bar_font.go $(FONT_RASTER_SRC) go.mod go.sum
	CGO_ENABLED=0 GOOS=$$(go env GOHOSTOS) GOARCH=$$(go env GOHOSTARCH) go run platform_dependent/fb/generate_system_bar_font.go -font Monaco_Linux.ttf -output $@

$(OUTPUT_DIR)/st-fb: $(FT_A) $(POPPLER_LIB) $(WEBP_LIB) $(WEBP_DEMUX) $(WEBP_ANIM_O) $(PDF_BRIDGE_O) $(IMAGE_DECODE_O) $(FFMPEG_STAMP) $(FB_STATUS_FONT) $(GO_SRC)
	go build -trimpath -tags 'backend_fb ffmpeg_static netgo osusergo $(FULL_GO_TAGS)' -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(FULL_EXTRA)"' .

$(OUTPUT_DIR)/st-fb-min: $(FB_STATUS_FONT) $(GO_SRC)
	CGO_ENABLED=0 go build -trimpath -tags 'purego backend_fb netgo osusergo $(TAB_GO_TAGS)' -o $@ .

$(OUTPUT_DIR)/st-min: $(GO_SRC)
	CGO_ENABLED=0 go build -trimpath -tags 'purego netgo osusergo $(TAB_GO_TAGS)' -o $@ .

# --- Go file-browser demo --------------------------------------------------
# Independent TUI module. TeX/Markdown previews are vector PDFs from latexpdf;
# the terminal displays them through DCS, and owns MP3 playback too.
# Run it inside the terminal:
#   ./BINARIES/st -e ./BINARIES/file-browser /path/to/files
$(FILE_BROWSER): $(FB_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/file-browser build -trimpath -o $(abspath $@) .

# --- GoLS static-corpus browser -------------------------------------------
# Read-only terminal UI over one or more lecture-server corpus databases.
# It shares latexpdf with lecture-server and asks st-go to display generated
# vector PDFs through the existing DCS open protocol; no HTTP server is used.
$(OUTPUT_DIR)/gols: $(GOLS_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/gols build -trimpath -o $(abspath $@) .

# PDF viewer; Poppler and the pixel cache live in the terminal, not this TUI.
$(OUTPUT_DIR)/zat: $(ZAT_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/zat build -trimpath -o $(abspath $@) .

# DjVu viewer; the pinned static DjVuLibre decoder lives in this TUI.
$(OUTPUT_DIR)/zat-djvu: Makefile $(DJVU_LIB) $(DJVU_STAMP) $(ZAT_DJVU_SRC) | $(OUTPUT_DIR)
	go -C demo-go/zat-djvu build -trimpath -tags 'djvu_static' -o $(abspath $@) \
		-ldflags '-linkmode external -extldflags "-static $(GC_EXTRA)"' .

# Raster image viewer; decoding and retained pixels live in the terminal.
$(OUTPUT_DIR)/goiv: $(GOIV_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/goiv build -trimpath -o $(abspath $@) .

# Native Go TUI editor, with terminal clipboard protocol and recovery journal.
$(OUTPUT_DIR)/govim: $(GOVIM_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/govim build -trimpath -o $(abspath $@) .

# OpenCode V2 API client with a retained Markdown/LaTeX response pane.
$(OUTPUT_DIR)/opencode-tui: $(OPENCODE_TUI_GO_SRC)
	CGO_ENABLED=0 go -C demo-go/opencode-tui build -trimpath -o $(abspath $@) .

# --- pdf bridge object (real poppler bridge, compiled outside cgo) --------
$(PDF_BRIDGE_O): third_party_wrapper/pdf_bridge.cpp pdf_bridge.h $(POPPLER_LIB)
	$(CXX) -std=c++20 -O2 -fPIC -Ithird_party/poppler/include \
	       -I$(POPPLER_SRC) -I$(POPPLER_SRC)/poppler -I$(POPPLER_BUILD) -I$(POPPLER_BUILD)/poppler -I. -c $< -o $@

# Full profiles prefer these bounded libpng/libjpeg-turbo decoders. The Go
# decoder remains available as a fallback for malformed and other formats.
$(IMAGE_DECODE_O): third_party_wrapper/image_decode.c $(PNG_LIB) $(JPEG_LIB)
	$(CC) -O2 -fPIC -ffunction-sections -fdata-sections \
	       -Ithird_party/poppler/include -I$(JPEG_DIR)/include -c $< -o $@

install: st
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BIN)" "$(DESTDIR)$(BINDIR)/st"
	install -m 0644 "$(CFG)" "$(DESTDIR)$(BINDIR)/st.json"
	TERMINFO="$(DESTDIR)$(TERMINFO_DIR)" ./$(BIN) -install-terminfo >/dev/null

# Only the root graphical host needs the decoder link flags. Nested modules
# are tested explicitly: Go's ./... pattern never crosses module boundaries.
TEST_EXTRA = $(FULL_EXTRA)

test: $(FT_A) $(POPPLER_LIB) $(WEBP_LIB) $(WEBP_DEMUX) $(PDF_BRIDGE_O) $(IMAGE_DECODE_O) $(WEBP_ANIM_O) st-web st-fb file-browser test-modules test-web test-fb-recording
	go test -tags '$(FULL_GO_TAGS)' ./... -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(TEST_EXTRA)"' -run 'Test[^Render]' -count=1

# Integrated profile, including the in-process pure-Go Pinyin tests.
test-modules:
	@set -e; for module in $(GO_MODULES); do \
		GOWORK=off go -C "$$module" test ./... -count=1; \
	done

# Standard Go workflow: no graphical libraries or Make-built assets needed.
# File-browser/GoLS use pure-Go SQLite and need no C compiler or ALSA.
test-tui:
	@set -e; for module in $(filter-out functionalities/audio,$(GO_MODULES)); do \
		GOWORK=off go -C "$$module" test ./... -count=1; \
	done

# Optional live canvas check using installed Chromium and Node's built-in CDP.
test-web: st-web
	CGO_ENABLED=0 go test -tags 'backend_web' ./platform_dependent/web ./platform_neutral/sharedclipboard
	CGO_ENABLED=0 go test -tags 'backend_web terminal_tabs' ./platform_neutral/frontend -run '^TestWebService'

test-web-browser: st-web govim file-browser
	node platform_dependent/web/testdata/browser-smoke.mjs
	node platform_dependent/web/testdata/service-smoke.mjs

# End-to-end animated-WebP playback through the built browser, PTY, terminal
# timer and X framebuffer. Point DISPLAY at an isolated X server.
test-webp-x11: st file-browser
	ST_TEST_WEBP_X11=1 ST_TEST_BINARY=./$(BIN) ST_TEST_FILE_BROWSER=./$(FILE_BROWSER) \
	go test -tags '$(FULL_GO_TAGS)' ./platform_neutral/frontend -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(TEST_EXTRA)"' \
		-run '^TestWebPAnimationX11FileBrowserPlayback$$' -count=1 -v

# Built terminal + zat, real Shift-Tab/mouse/Ctrl-Shift-C and X clipboard, with
# host UTF-16 conversion modules deliberately unavailable.
test-zat-x11: st zat
	ST_TEST_ZAT_X11=1 ST_TEST_BINARY=./$(BIN) ST_TEST_ZAT=./$(OUTPUT_DIR)/zat \
	go test -tags '$(FULL_GO_TAGS)' ./platform_neutral/frontend -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(FULL_EXTRA)"' \
		-run '^TestZat(PageText|Search)X11$$' -count=1 -v

test-zat-djvu: $(DJVU_LIB) $(DJVU_STAMP)
	GOWORK=off go -C demo-go/zat-djvu test -tags 'djvu_static' \
		-ldflags '-linkmode external -extldflags "-static $(GC_EXTRA)"' ./...

test-fb-recording: $(FFMPEG_STAMP)
	CGO_ENABLED=0 go test ./platform_dependent/fb
	go test -tags ffmpeg_static ./platform_dependent/fb

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/st" "$(DESTDIR)$(BINDIR)/st.json"
	rm -f "$(DESTDIR)$(TERMINFO_DIR)/s/st-256color" \
	      "$(DESTDIR)$(TERMINFO_DIR)/s/stterm-256color"

clean:
	rm -f $(BINARY_PATHS) $(OUTPUT_DIR)/govim-pinyind

distclean: clean
	rm -rf third_party

.PHONY: all install uninstall clean distclean test test-modules test-tui test-web test-web-browser test-webp-x11 test-zat-x11 test-zat-djvu test-fb-recording $(BINARY_NAMES) terminal-profile-force
