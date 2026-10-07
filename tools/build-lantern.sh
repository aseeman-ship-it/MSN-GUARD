#!/usr/bin/env bash
# usage: tools/build-lantern.sh <source-dir> <go-package>
set -euo pipefail

SRC="${1:?source dir}"
PKG="${2:-./cmd/lanternd}"
OUT="$(pwd)/out"
NDK="${ANDROID_NDK_LATEST_HOME:-${ANDROID_NDK_HOME:?NDK not found}}"
TC="$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin"
API=24
TAGS="${BUILD_TAGS:-}"

mkdir -p "$OUT/arm64-v8a" "$OUT/armeabi-v7a"
cd "$SRC"
go mod download

build() { # arch goarm cc outdir
  CGO_ENABLED=1 GOOS=android GOARCH="$1" GOARM="$2" CC="$3" \
  go build -trimpath -tags "$TAGS" \
    -ldflags "-s -w -extldflags '-Wl,-z,max-page-size=16384'" \
    -o "$OUT/$4/liblantern.so" "$PKG"
}

build arm64 "" "$TC/aarch64-linux-android${API}-clang"    arm64-v8a
build arm   7  "$TC/armv7a-linux-androideabi${API}-clang" armeabi-v7a

file "$OUT"/*/liblantern.so
ls -lh "$OUT"/*/liblantern.so
