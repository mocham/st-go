#! /usr/bin/bash
# Build st-go against the statically-linked FreeType2 (fetched by the
# Makefile into third_party/ when missing). X is pure-Go via xgb.
# No fontconfig, no Xft, no dynamic dependencies.

set -e
cd "$(dirname "$0")"
make "$@"
