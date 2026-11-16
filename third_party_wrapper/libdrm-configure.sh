#!/bin/sh
# Build only the portable core DRM/KMS API used by gov2. Feature autodetection
# is disabled so host GPU libraries, Cairo and Valgrind cannot enter the build.
set -eu
exec meson setup "$1" "$2" --prefix="$3" --libdir=lib \
    --buildtype=release --default-library=static --auto-features=disabled \
    --wrap-mode=nodownload -Doptimization=2 -Db_staticpic=true \
    -Dc_args="-ffunction-sections -fdata-sections" \
    -Dtests=false -Dinstall-test-programs=false -Dudev=true
