# st-go: a Golang clone of suckless st.
#
# Third-party static libraries (all downloaded & built into third_party/ on
# demand; the whole folder is gitignored and reproduced on demand):
#   - FreeType2   : glyph rasterization
#   - stb_image   : PNG/JPEG/GIF/BMP image decode
#   - libwebp     : WebP decode (stb_image has no WebP support)
#   - poppler     : PDF rendering (first page / page N via "open")
#   - zlib        : poppler's stream decompression
#   - libpng      : poppler's embedded-image support
#   - libjpeg-turbo: poppler's JPEG/DCT image decoder
# SQLite comes from go-sqlite3's bundled amalgamation rather than third_party/.
#
# poppler uses the C++/Splash renderer with static freetype, zlib, libpng and
# libjpeg-turbo. The JPEG decoder uses libjpeg.a, not the TurboJPEG API.
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

# stb_image: single-header, downloaded into third_party/ (pure third-party
# code). The wrapper .c in third_party_wrapper/ (our glue) defines the
# implementation and is precompiled into a .o so the ~280 KB single-header is
# not recompiled on every build.
STB_DIR     = third_party/stb
STB_H       = $(STB_DIR)/stb_image.h
STB_O       = $(STB_DIR)/stb_image.o
STB_C       = third_party_wrapper/stb_image.c
STB_URL     = https://raw.githubusercontent.com/nothings/stb/master/stb_image.h

OUTPUT_DIR  = BINARIES
BIN         = $(OUTPUT_DIR)/st
BINARY_NAMES = st st-min st-stb st-pdf file-browser gols vimtex zat govim govim-pinyind
BINARY_PATHS = $(addprefix $(OUTPUT_DIR)/,$(BINARY_NAMES))
CFG         = config/config.json
PREFIX     ?= /usr/local
BINDIR     ?= $(PREFIX)/bin
TERMINFO_DIR ?= $(PREFIX)/share/terminfo

# SQLite is only used by the full terminal. Omit it from reduced builds and
# disable runtime extension loading so the static binary has no dlopen
# dependency from SQLite.
FULL_GO_TAGS = webpcache sqlite_omit_load_extension

# Sources that affect the Go file-browser demo, including the sibling
# latexpdf module and semantic-HTML renderer. The target links the existing
# static ALSA wrapper below.
FILE_BROWSER = $(OUTPUT_DIR)/file-browser
LATEXPDF_SRC = $(wildcard latexpdf/*.go) \
	       $(wildcard latexpdf/pdf_assets/*)
SEMHTML_SRC = $(wildcard semhtml/*.go)
TUI_NAV_SRC = $(wildcard tui/navigation/*.go)
TUI_SEARCH_SRC = $(wildcard tui/textsearch/*.go)
TUI_READLINE_SRC = $(wildcard tui/readline/*.go)
STTERMINAL_SRC = $(wildcard term/*.go platform_neutral/stterminal/*.go)
FB_GO_SRC    = $(wildcard demo-go/file-browser/*.go) \
	       $(wildcard demo-go/file-browser/*.conf) go.mod go.sum $(LATEXPDF_SRC) $(SEMHTML_SRC) $(TUI_NAV_SRC) $(TUI_READLINE_SRC)
GOLS_GO_SRC  = $(wildcard demo-go/gols/*.go) go.mod go.sum $(LATEXPDF_SRC) $(TUI_NAV_SRC) $(TUI_SEARCH_SRC) $(TUI_READLINE_SRC) $(wildcard pdfmeta/*.go) $(wildcard limits/*.go)
VIMTEX_GO_SRC = $(wildcard demo-go/vimtex/*.go) $(wildcard demo-go/vimtex/*.vim) $(wildcard ptyutil/*.go) \
	       $(wildcard limits/*.go) go.mod go.sum $(LATEXPDF_SRC)
ZAT_GO_SRC = $(wildcard demo-go/zat/*.go) $(wildcard limits/*.go) $(wildcard pdfmeta/*.go) \
	       $(STTERMINAL_SRC) $(wildcard config/*.go) $(wildcard config/*.json) $(TUI_NAV_SRC) $(TUI_SEARCH_SRC) $(TUI_READLINE_SRC) go.mod go.sum
GOVIM_GO_SRC = $(wildcard demo-go/govim/*.go demo-go/govim/syntax/* demo-go/govim/imap/* tui/imap/*.go tui/preview/*.go tui/outline/*.go config/*.go config/*.json) $(STTERMINAL_SRC) $(TUI_READLINE_SRC) $(LATEXPDF_SRC) $(wildcard limits/*.go) go.mod go.sum

# Google Pinyin decoder only: no SCIM, GTK, X11 or installed IM framework.
PINYIN_REV = 0b9407ef8e4e5fbf74d5ff4e42ea20025a1483fc
PINYIN_TAR = third_party/scim-googlepinyin-$(PINYIN_REV).tar.gz
PINYIN_SRC = third_party/googlepinyin-src
PINYIN_LIB = third_party/googlepinyin/libpinyinime.a
PINYIN_DATA = demo-go/pinyind/internal/pinyinengine/assets/dict_pinyin.dat
PINYIN_EMBED ?= 1
ifeq ($(PINYIN_EMBED),1)
PINYIN_GO_TAGS = googlepinyin
PINYIN_ASSET_DEP = $(PINYIN_DATA)
else
PINYIN_GO_TAGS = googlepinyin,pinyin_external
PINYIN_ASSET_DEP =
endif
PINYIN_NAMES = dictbuilder dictlist dicttrie lpicache matrixsearch mystdlib ngram pinyinime searchutility spellingtable spellingtrie splparser sync userdict utf16char utf16reader
PINYIN_BUILDER_NAMES = dictbuilder dictlist dicttrie lpicache mystdlib ngram searchutility spellingtable spellingtrie splparser utf16char utf16reader

$(PINYIN_TAR):
	mkdir -p third_party
	curl --fail --location --silent --show-error https://codeload.github.com/tchaikov/scim-googlepinyin/tar.gz/$(PINYIN_REV) -o $@.next
	printf '%s  %s\n' 60f1c4396807e27ba9952611b3ffd523c6773e1ea910b158fee9bccaf6d0327e $@.next | sha256sum -c -
	mv $@.next $@

$(PINYIN_SRC)/.unpacked: $(PINYIN_TAR)
	printf '%s  %s\n' 60f1c4396807e27ba9952611b3ffd523c6773e1ea910b158fee9bccaf6d0327e $< | sha256sum -c -
	mkdir -p $(PINYIN_SRC)
	tar -xzf $< -C $(PINYIN_SRC) --strip-components=1
	touch $@

$(PINYIN_LIB): $(PINYIN_SRC)/.unpacked
	mkdir -p $(@D)
	set -e; for name in $(PINYIN_NAMES); do $(CXX) -O2 -std=c++11 -pthread -c $(PINYIN_SRC)/src/share/$$name.cpp -o $(@D)/$$name.o; done
	$(AR) rcs $@ $(addprefix $(@D)/,$(addsuffix .o,$(PINYIN_NAMES)))

third_party/googlepinyin/dictbuilder: $(PINYIN_SRC)/.unpacked
	mkdir -p $(@D)
	$(CXX) -O2 -std=c++11 -pthread -D___BUILD_MODEL___ $(PINYIN_SRC)/src/command/pinyinime_dictbuilder.cpp $(addprefix $(PINYIN_SRC)/src/share/,$(addsuffix .cpp,$(PINYIN_BUILDER_NAMES))) -o $@

$(PINYIN_DATA): third_party/googlepinyin/dictbuilder
	cd $(PINYIN_SRC)/src/command && $(abspath $<)
	mkdir -p $(@D)
	cp $(PINYIN_SRC)/src/data/dict_pinyin.dat $@
	cp $(PINYIN_SRC)/COPYING $(@D)/LICENSE

$(OUTPUT_DIR)/govim-pinyind: $(PINYIN_LIB) $(PINYIN_ASSET_DEP) $(wildcard demo-go/pinyind/*.go demo-go/pinyind/internal/pinyinengine/*.* tui/pinyin/*.go) go.mod go.sum Makefile pinyin-force | $(OUTPUT_DIR)
	CGO_ENABLED=1 go build -trimpath -tags 'netgo,osusergo,$(PINYIN_GO_TAGS)' -ldflags '-linkmode external -extldflags "-static -pthread"' -o $@ ./demo-go/pinyind

pinyin-assets: $(PINYIN_DATA)

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

# libwebp: WebP decode (stb_image has no WebP support).
WEBP_VERSION = 1.6.0
WEBP_URL     = https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-$(WEBP_VERSION).tar.gz
WEBP_TAR     = third_party/libwebp-$(WEBP_VERSION).tar.gz
WEBP_SRC     = third_party/libwebp-src
WEBP_LIB     = third_party/webp/lib/libwebp.a
WEBP_DEMUX   = third_party/webp/lib/libwebpdemux.a
WEBP_ANIM_C  = third_party_wrapper/webp_anim.c
WEBP_ANIM_O  = third_party/webp/webp_anim.o

# ALSA (libasound): audio playback for the Go file-browser's mp3 viewer.
# Built statically following the gowidgets/CLibBuild recipe, including its
# static-linking patches: dlopen() and NSS lookups (getgrnam_r/getpwuid_r)
# are unavailable in a statically linked binary, so they are neutralized.
ALSA_VERSION = 1.2.14
ALSA_URL     = https://www.alsa-project.org/files/pub/lib/alsa-lib-$(ALSA_VERSION).tar.bz2
ALSA_TAR     = third_party/alsa-lib-$(ALSA_VERSION).tar.bz2
ALSA_SRC     = third_party/alsa-lib-src
ALSA_DIR     = third_party/alsa
ALSA_LIB     = third_party/alsa/lib/libasound.a
SND_C        = third_party_wrapper/alsa_snd.c
SND_O        = third_party/alsa/snd.o

all: $(BIN)

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

# --- stb_image ----------------------------------------------------------
# stb_image.h is header-only third-party code; fetch it into third_party/
# if missing, then compile our wrapper (which defines STB_IMAGE_IMPLEMENTATION)
# into a .o so the ~280 KB single-header is not recompiled on every build.
$(STB_O): $(STB_C) $(STB_H)
	$(CC) -O2 -ffunction-sections -fdata-sections -I$(STB_DIR) -c "$(STB_C)" -o "$@"

$(STB_H):
	@mkdir -p "$(STB_DIR)"
	@if [ ! -f "$(STB_H)" ]; then \
		echo "downloading stb_image.h..."; \
		curl -fL -o "$(STB_H)" "$(STB_URL)"; \
	fi

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

# --- libpng static (poppler dep for embedded images) ---------------------
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

# --- libjpeg-turbo static (poppler DCTDecode/JPEG support) ----------------
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

# --- libwebp static (WebP decode; stb_image has no WebP support) ----------
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
# like alsa_snd.o so the cgo file only declares extern functions.
$(WEBP_ANIM_O): $(WEBP_ANIM_C) $(WEBP_LIB) $(WEBP_DEMUX)
	@mkdir -p "$(dir $@)"
	$(CC) -O2 -fPIC -Ithird_party/webp/include -c "$<" -o "$@"

# --- ALSA static (libasound; audio playback for the Go file-browser) ------
# Static build mirroring gowidgets/CLibBuild. dlopen() and the NSS lookups
# getgrnam_r/getpwuid_r do not work in a statically linked binary, so they are
# patched out (plugin loading via dlopen is replaced by "not found"; NSS
# lookups degrade to "no such user/group").
$(ALSA_LIB):
	@mkdir -p third_party
	@if [ ! -f "$(ALSA_TAR)" ]; then \
		echo "downloading alsa-lib $(ALSA_VERSION)..."; \
		curl -fL -o "$(ALSA_TAR)" "$(ALSA_URL)"; \
	fi
	rm -rf "$(ALSA_SRC)" "$(ALSA_DIR)"
	mkdir -p "$(ALSA_SRC)" "$(ALSA_DIR)/lib" "$(ALSA_DIR)/include"
	tar -xjf "$(ALSA_TAR)" -C "$(ALSA_SRC)" --strip-components=1
	sed -i 's|handle = dlopen(filename, mode);|printf("name=%s\\npath=%s\\n", name, filename);handle = NULL;|' "$(ALSA_SRC)/src/dlmisc.c"
	sed -i 's|int st = getgrnam_r(.*);|int st=-1;|' "$(ALSA_SRC)/src/pcm/pcm_direct.c"
	sed -i '/getpwuid_r/ c\goto out;while(0){' "$(ALSA_SRC)/src/userfile.c"
	sed -i '/dlopen/ c\handle=NULL;' "$(ALSA_SRC)/src/pcm/pcm_ladspa.c"
	cd "$(ALSA_SRC)" && ./configure --enable-shared=no --enable-static=yes CFLAGS="-fPIC -O2"
	$(MAKE) -C "$(ALSA_SRC)"
	cp "$(ALSA_SRC)/src/.libs/libasound.a" "$(ALSA_LIB)"
	cp -r "$(ALSA_SRC)/include/." "$(ALSA_DIR)/include/"

# ALSA playback wrapper, precompiled like pdf_bridge.o so the cgo file only
# needs extern declarations (no ALSA headers at Go-compile time).
$(SND_O): $(SND_C) $(ALSA_LIB)
	@mkdir -p "$(dir $@)"
	$(CC) -O2 -fPIC -Ithird_party/alsa/include -c "$<" -o "$@"

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

# --- st targets ------------------------------------------------------------
# Four build levels with an increasing set of third-party libraries:
#   st      (full) : freetype + stb_image + libwebp + poppler
#   st-pdf         : freetype + stb_image + poppler          (no webp)
#   st-stb         : freetype + stb_image                    (no pdf/webp)
#   st-min         : freetype only                           (no image/pdf)
#
# When a library is dropped, the cgo files still reference its C symbols; the
# corresponding dummy-*.o (a no-op that returns failure) satisfies the link, so
# the code degrades gracefully (shows nothing) instead of crashing. All extra
# objects/libs are passed via -extldflags so a single set of .go files works
# for every level.

DUMMY_DIR    = third_party/dummy
DUMMY_STB    = $(DUMMY_DIR)/dummy-stb.o
DUMMY_WEBP   = $(DUMMY_DIR)/dummy-webp.o
DUMMY_PDF    = $(DUMMY_DIR)/dummy-pdf.o
PDF_BRIDGE_O = third_party/pdf_bridge.o

# extra link items appended to -extldflags (order matters for static libs;
# pdf_bridge.o must precede -lpoppler-cpp so --gc-sections keeps the needed
# poppler objects; --gc-sections relies on the libs being built with
# -ffunction-sections -fdata-sections).
MIN_EXTRA  = $(DUMMY_STB) $(DUMMY_WEBP) $(DUMMY_PDF)
STB_EXTRA  = $(STB_O) -lm $(DUMMY_WEBP) $(DUMMY_PDF)
PDF_EXTRA  = $(STB_O) -lm $(PDF_BRIDGE_O) -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler $(JPEG_LIB) -Lthird_party/poppler/lib -lpng16 -lz -Lthird_party/freetype -lfreetype -lstdc++ -lm $(DUMMY_WEBP)
FULL_EXTRA = $(STB_O) -lm $(PDF_BRIDGE_O) -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler $(JPEG_LIB) -Lthird_party/poppler/lib -lpng16 -lz -Lthird_party/freetype -lfreetype -lstdc++ -lm $(WEBP_ANIM_O) -Lthird_party/webp/lib -l:libwebpdemux.a $(WEBP_LIB)

# ALSA link items for the Go file-browser (libasound + the playback wrapper).
# snd.o must precede -l:libasound.a. -ldl/-lpthread are no-ops on modern glibc
# where they are merged into libc.
ALSA_EXTRA = $(SND_O) -Lthird_party/alsa/lib -l:libasound.a -ldl -lpthread -lm

# Animated-WebP items for the Go file-browser (WebPAnimDecoder playback).
# webp_anim.o must precede -l:libwebpdemux.a; libwebp.a comes via WEBP_LIB.
WEBP_ANIM_EXTRA = $(WEBP_ANIM_O) -Lthird_party/webp/lib -l:libwebpdemux.a -l:libwebp.a

# --gc-sections drops unreferenced functions from function-sections objects.
GC_EXTRA   = -Wl,--gc-sections

PLATFORM_SRC := $(wildcard platform_neutral/*/*.go platform_dependent/*/*.go)
GO_SRC := $(wildcard *.go) $(wildcard term/*.go) $(wildcard config/*.go) $(PLATFORM_SRC) \
	  $(wildcard pdfmeta/*.go) \
	  $(wildcard limits/*.go) \
          $(wildcard config/*.json) $(wildcard ptyutil/*.go) $(wildcard terminfo/*.go) \
          $(wildcard third_party_wrapper/*.c) $(wildcard third_party_wrapper/*.h) \
          $(wildcard third_party_wrapper/*.cpp) $(wildcard *.h)

# Rebuild when build flags change; trim source and module-cache paths from all
# distributable Go binaries, including their stack-trace/debug metadata.
$(BINARY_PATHS): Makefile | $(OUTPUT_DIR)

$(OUTPUT_DIR):
	mkdir -p $@

# Keep familiar make target names, but never create executables in the root.
$(BINARY_NAMES): %: $(OUTPUT_DIR)/%

pinyin-force:

$(OUTPUT_DIR)/st: $(FT_A) $(STB_O) $(POPPLER_LIB) $(WEBP_LIB) $(WEBP_DEMUX) $(WEBP_ANIM_O) $(PDF_BRIDGE_O) $(GO_SRC)
	go build -trimpath -tags '$(FULL_GO_TAGS)' -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(FULL_EXTRA)"' .

$(OUTPUT_DIR)/st-pdf: $(FT_A) $(STB_O) $(POPPLER_LIB) $(PDF_BRIDGE_O) $(DUMMY_WEBP) $(GO_SRC)
	go build -trimpath -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(PDF_EXTRA)"' .

$(OUTPUT_DIR)/st-stb: $(FT_A) $(STB_O) $(DUMMY_WEBP) $(DUMMY_PDF) $(GO_SRC)
	go build -trimpath -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(STB_EXTRA)"' .

$(OUTPUT_DIR)/st-min: $(FT_A) $(DUMMY_STB) $(DUMMY_WEBP) $(DUMMY_PDF) $(GO_SRC)
	go build -trimpath -o $@ -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(MIN_EXTRA)"' .

# --- Go file-browser demo --------------------------------------------------
# A Go clone of demo/file-browser.sh. It links the static ALSA library for the
# mp3 viewer (embedded alsa.conf + go-mp3 decoded PCM playback). TeX previews
# are vector PDFs rendered by latexpdf; Markdown previews remain paginated PNGs
# composed with mathjax-go. The terminal displays them through the DCS DSL.
# Run it inside the terminal:
#   ./BINARIES/st -e ./BINARIES/file-browser /path/to/files
$(FILE_BROWSER): $(FB_GO_SRC) $(ALSA_LIB) $(SND_O)
	go build -trimpath -o $@ -ldflags '-linkmode external -extldflags "-static $(ALSA_EXTRA)"' ./demo-go/file-browser

# --- GoLS static-corpus browser -------------------------------------------
# Read-only terminal UI over one or more lecture-server corpus databases.
# It shares latexpdf with lecture-server and asks st-go to display generated
# vector PDFs through the existing DCS open protocol; no HTTP server is used.
$(OUTPUT_DIR)/gols: $(GOLS_GO_SRC)
	go build -trimpath -tags 'sqlite_omit_load_extension' -o $@ \
		-ldflags '-linkmode external -extldflags "-static $(GC_EXTRA)"' ./demo-go/gols

# Real Vim on a pane-sized PTY, with save-triggered LaTeX PDF previews.
$(OUTPUT_DIR)/vimtex: $(VIMTEX_GO_SRC)
	CGO_ENABLED=0 go build -trimpath -o $@ ./demo-go/vimtex

# PDF viewer; Poppler and the pixel cache live in the terminal, not this TUI.
$(OUTPUT_DIR)/zat: $(ZAT_GO_SRC)
	CGO_ENABLED=0 go build -trimpath -o $@ ./demo-go/zat

# Native Go TUI editor, with terminal clipboard protocol and recovery journal.
$(OUTPUT_DIR)/govim: $(GOVIM_GO_SRC) $(wildcard tui/pinyin/*.go) $(OUTPUT_DIR)/govim-pinyind
	CGO_ENABLED=0 go build -trimpath -o $@ ./demo-go/govim

# --- dummy objects (no-op stubs for dropped libraries) --------------------
$(DUMMY_DIR)/dummy-stb.o: third_party_wrapper/dummy-stb.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -ffunction-sections -fdata-sections -c $< -o $@

$(DUMMY_DIR)/dummy-webp.o: third_party_wrapper/dummy-webp.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -ffunction-sections -fdata-sections -c $< -o $@

$(DUMMY_DIR)/dummy-pdf.o: third_party_wrapper/dummy-pdf.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -ffunction-sections -fdata-sections -c $< -o $@

# --- pdf bridge object (real poppler bridge, compiled outside cgo) --------
$(PDF_BRIDGE_O): third_party_wrapper/pdf_bridge.cpp pdf_bridge.h $(POPPLER_LIB)
	$(CXX) -std=c++20 -O2 -fPIC -Ithird_party/poppler/include \
	       -I$(POPPLER_SRC) -I$(POPPLER_SRC)/poppler -I$(POPPLER_BUILD) -I$(POPPLER_BUILD)/poppler -I. -c $< -o $@

install: st
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BIN)" "$(DESTDIR)$(BINDIR)/st"
	install -m 0644 "$(CFG)" "$(DESTDIR)$(BINDIR)/st.json"
	TERMINFO="$(DESTDIR)$(TERMINFO_DIR)" ./$(BIN) -install-terminfo >/dev/null

# go test ./... needs the third-party libs linked too (they moved out of the
# #cgo LDFLAGS into -extldflags); test against the full build. The ALSA items
# are needed because the Go file-browser package uses cgo.
TEST_EXTRA = $(FULL_EXTRA) $(ALSA_EXTRA)

test: $(FT_A) $(STB_O) $(POPPLER_LIB) $(WEBP_LIB) $(WEBP_DEMUX) $(PDF_BRIDGE_O) $(ALSA_LIB) $(SND_O) $(WEBP_ANIM_O) govim-pinyind
	go test -tags '$(FULL_GO_TAGS)' ./... -ldflags '-linkmode external -extldflags "-static $(GC_EXTRA) $(TEST_EXTRA)"' -run 'Test[^Render]' -count=1

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

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/st" "$(DESTDIR)$(BINDIR)/st.json"
	rm -f "$(DESTDIR)$(TERMINFO_DIR)/s/st-256color" \
	      "$(DESTDIR)$(TERMINFO_DIR)/s/stterm-256color"

clean:
	rm -f $(BINARY_PATHS) $(PINYIN_DATA)

distclean: clean
	rm -rf third_party

.PHONY: all install uninstall clean distclean test test-webp-x11 test-zat-x11 $(BINARY_NAMES) pinyin-assets pinyin-force
