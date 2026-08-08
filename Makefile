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
#
# poppler is built MINIMAL: only the C++ API (page_renderer -> raw BGRA) is
# linked, so cairo/glib/gobject/ffi/pixman/lcms/openjpeg/turbojpeg are NOT
# needed (the bloat that poppler's glib API would pull in). Only freetype +
# zlib + libpng are required.
#
# X is driven by the pure-Go xgb library over sockets, so no X11 C library
# is needed. The result is a fully static binary with no dynamic deps.

VERSION     = 0.9.2
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

BIN         = st
CFG         = config/config.json
PREFIX     ?= /usr/local
BINDIR     ?= $(PREFIX)/bin

# --- poppler (PDF rendering) ---------------------------------------------
# A MINIMAL static poppler using only the C++ API (page_renderer -> raw BGRA),
# so we do NOT need cairo/glib/gobject/ffi/pixman/lcms/openjpeg/turbojpeg
# (the bloat that poppler's glib API pulls in). Only freetype (already in
# third_party/) + zlib + libpng are required.
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

# libwebp: WebP decode (stb_image has no WebP support).
WEBP_VERSION = 1.6.0
WEBP_URL     = https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-$(WEBP_VERSION).tar.gz
WEBP_TAR     = third_party/libwebp-$(WEBP_VERSION).tar.gz
WEBP_SRC     = third_party/libwebp-src
WEBP_LIB     = third_party/webp/lib/libwebp.a

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
		--without-brotli --without-bzip2
	$(MAKE) -C "$(FT_SRC)"
	mkdir -p "$(FT_DIR)"
	cp "$(FT_SRC)/objs/.libs/libfreetype.a" "$(FT_A)"
	cp -r "$(FT_SRC)/include" "$(FT_DIR)/include"

# --- stb_image ----------------------------------------------------------
# stb_image.h is header-only third-party code; fetch it into third_party/
# if missing, then compile our wrapper (which defines STB_IMAGE_IMPLEMENTATION)
# into a .o so the ~280 KB single-header is not recompiled on every build.
$(STB_O): $(STB_C) $(STB_H)
	$(CC) -O2 -I$(STB_DIR) -c "$(STB_C)" -o "$@"

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
	cd "$(ZLIB_SRC)" && ./configure --static --prefix=$$PWD/out
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
	cd "$(PNG_SRC)" && ./configure --disable-shared --enable-static CFLAGS="-fPIC -O2" LDFLAGS="-L$$PWD/../poppler/lib"
	$(MAKE) -C "$(PNG_SRC)"
	cp "$(PNG_SRC)/.libs/libpng16.a" "$(PNG_LIB)"
	cp "$(PNG_SRC)/png.h" "$(PNG_SRC)/pngconf.h" "$(PNG_SRC)/pnglibconf.h" third_party/poppler/include/

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
	cd "$(WEBP_SRC)" && ./configure CFLAGS="-fPIC -O2" --enable-static --disable-shared
	$(MAKE) -C "$(WEBP_SRC)"
	# the webp tarball ships a swig/libwebp.go which would make "go test ./..."
	# try to build it as a Go package; drop it (and other non-C build dirs).
	rm -rf "$(WEBP_SRC)/swig" "$(WEBP_SRC)/man" "$(WEBP_SRC)/extras"
	cp "$(WEBP_SRC)/src/.libs/libwebp.a" "$(WEBP_LIB)"
	cp "$(WEBP_SRC)/src/webp/"*.h third_party/webp/include/webp/

# --- poppler static (minimal, C++ API only) ------------------------------
$(POPPLER_LIB): $(FT_A) $(ZLIB_LIB) $(PNG_LIB)
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
		-DCMAKE_CXX_FLAGS="-fPIC" -DCMAKE_C_FLAGS="-fPIC" \
		-DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
		-DFREETYPE_INCLUDE_DIRS="$(abspath third_party/freetype/include)" \
		-DFREETYPE_LIBRARY="$(abspath third_party/freetype/libfreetype.a)" \
		-DZLIB_INCLUDE_DIR="$(abspath third_party/poppler/include)" \
		-DZLIB_LIBRARY="$(abspath third_party/poppler/lib/libz.a)" \
		-DPNG_INCLUDE_DIR="$(abspath third_party/poppler/include)" \
		-DPNG_LIBRARY="$(abspath third_party/poppler/lib/libpng16.a)" \
		-DFONT_CONFIGURATION=generic \
		-DENABLE_GLIB=OFF -DENABLE_QT5=OFF -DENABLE_QT6=OFF \
		-DENABLE_BOOST=OFF -DENABLE_CAIRO=OFF -DENABLE_LCMS=OFF \
		-DENABLE_LIBCURL=OFF -DENABLE_LIBTIFF=OFF -DENABLE_NSS3=OFF \
		-DENABLE_GPGME=OFF -DENABLE_LIBOPENJPEG=none -DENABLE_DCTDECODER=none \
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

# extra link items appended to -extldflags (order matters for static libs)
MIN_EXTRA  = $(DUMMY_STB) $(DUMMY_WEBP) $(DUMMY_PDF)
STB_EXTRA  = $(STB_O) -lm $(DUMMY_WEBP) $(DUMMY_PDF)
PDF_EXTRA  = $(STB_O) -lm $(PDF_BRIDGE_O) -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler -Lthird_party/poppler/lib -lpng16 -lz -Lthird_party/freetype -lfreetype -lstdc++ -lm $(DUMMY_WEBP)
FULL_EXTRA = $(STB_O) -lm $(PDF_BRIDGE_O) -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler -Lthird_party/poppler/lib -lpng16 -lz -Lthird_party/freetype -lfreetype -lstdc++ -lm $(WEBP_LIB)

GO_SRC := $(wildcard *.go) $(wildcard term/*.go) $(wildcard config/*.go) \
          $(wildcard config/*.json) $(wildcard ptyutil/*.go) \
          $(wildcard third_party_wrapper/*.c) $(wildcard third_party_wrapper/*.h) \
          $(wildcard third_party_wrapper/*.cpp) $(wildcard *.h)

st: $(FT_A) $(STB_O) $(POPPLER_LIB) $(WEBP_LIB) $(PDF_BRIDGE_O) $(GO_SRC)
	go build -o $@ -ldflags '-linkmode external -extldflags "-static $(FULL_EXTRA)"' .

st-pdf: $(FT_A) $(STB_O) $(POPPLER_LIB) $(PDF_BRIDGE_O) $(DUMMY_WEBP) $(GO_SRC)
	go build -o $@ -ldflags '-linkmode external -extldflags "-static $(PDF_EXTRA)"' .

st-stb: $(FT_A) $(STB_O) $(DUMMY_WEBP) $(DUMMY_PDF) $(GO_SRC)
	go build -o $@ -ldflags '-linkmode external -extldflags "-static $(STB_EXTRA)"' .

st-min: $(FT_A) $(DUMMY_STB) $(DUMMY_WEBP) $(DUMMY_PDF) $(GO_SRC)
	go build -o $@ -ldflags '-linkmode external -extldflags "-static $(MIN_EXTRA)"' .

# --- dummy objects (no-op stubs for dropped libraries) --------------------
$(DUMMY_DIR)/dummy-stb.o: third_party_wrapper/dummy-stb.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -c $< -o $@

$(DUMMY_DIR)/dummy-webp.o: third_party_wrapper/dummy-webp.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -c $< -o $@

$(DUMMY_DIR)/dummy-pdf.o: third_party_wrapper/dummy-pdf.c
	@mkdir -p $(DUMMY_DIR)
	$(CC) -O2 -c $< -o $@

# --- pdf bridge object (real poppler bridge, compiled outside cgo) --------
$(PDF_BRIDGE_O): third_party_wrapper/pdf_bridge.cpp pdf_bridge.h
	$(CXX) -std=c++11 -fPIC -Ithird_party/poppler/include \
	       -Ithird_party/poppler/include/poppler -I. -c $< -o $@

install: st
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 st "$(DESTDIR)$(BINDIR)/st"
	install -m 0644 "$(CFG)" "$(DESTDIR)$(BINDIR)/st.json"

# go test ./... needs the third-party libs linked too (they moved out of the
# #cgo LDFLAGS into -extldflags); test against the full build.
TEST_EXTRA = $(FULL_EXTRA)

test: $(FT_A) $(STB_O) $(POPPLER_LIB) $(WEBP_LIB) $(PDF_BRIDGE_O)
	go test ./... -ldflags '-linkmode external -extldflags "-static $(TEST_EXTRA)"' -run 'Test[^Render]' -count=1

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/st" "$(DESTDIR)$(BINDIR)/st.json"

clean:
	rm -f st st-min st-stb st-pdf

distclean:
	rm -rf third_party st st-min st-stb st-pdf

.PHONY: all install uninstall clean distclean test
