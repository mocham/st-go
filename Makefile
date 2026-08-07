# st-go: a Golang clone of suckless st.
#
# FreeType2 is the only third-party library. It is downloaded, extracted,
# and built as a static archive into third_party/ the first time the binary
# is built (the whole folder is gitignored and reproduced on demand).
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
CFG         = config.json
PREFIX     ?= /usr/local
BINDIR     ?= $(PREFIX)/bin
LDFLAGS     = -linkmode external -extldflags "-static"

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

# --- st -----------------------------------------------------------------
$(BIN): $(FT_A) $(STB_O) $(wildcard *.go) $(wildcard term/*.go) $(wildcard config/*.go) \
        $(wildcard ptyutil/*.go) $(wildcard third_party_wrapper/*.c) $(wildcard third_party_wrapper/*.h)
	go build -o "$(BIN)" -ldflags "$(LDFLAGS)" .

install: $(BIN)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BIN)" "$(DESTDIR)$(BINDIR)/$(BIN)"
	install -m 0644 "$(CFG)" "$(DESTDIR)$(BINDIR)/$(BIN).json"

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/$(BIN)" "$(DESTDIR)$(BINDIR)/$(BIN).json"

clean:
	rm -f "$(BIN)"

distclean:
	rm -rf third_party "$(BIN)"

.PHONY: all install uninstall clean distclean
