#!/bin/sh
# Run in the extracted FFmpeg source directory. This tracked profile is a build
# dependency so adding codecs rebuilds archives without invalidating them on
# unrelated Makefile edits.
set -eu
prefix=$1
x264=$2
export PKG_CONFIG_PATH="$x264/lib/pkgconfig"
exec ./configure --prefix="$prefix" --pkg-config-flags=--static \
    --enable-static --disable-shared --enable-pic --enable-gpl --enable-libx264 \
    --disable-autodetect --disable-everything --disable-programs --disable-doc \
    --disable-debug --disable-network --disable-iconv --disable-avdevice \
    --enable-avformat --enable-avcodec --enable-avutil --enable-swscale --enable-swresample --enable-avfilter \
    --enable-filter=abuffer,abuffersink,atempo \
    --enable-encoder=libx264 --enable-muxer=mp4 --enable-protocol=file \
    --enable-demuxer=mov --enable-decoder=h264,hevc,mpeg4,aac,mp3 \
    --extra-cflags="-O2 -ffunction-sections -fdata-sections"
