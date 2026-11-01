#!/bin/sh
# probes/build.sh ARCH OUT_DIR: static probe binaries for amd64 or arm64.
#   core-probe, ipc-probe  C, static (host gcc for amd64; for arm64 set
#                          CC_ARM64 to a static-capable aarch64 compiler,
#                          e.g. aarch64-linux-musl-gcc or zig cc)
#   go-probe               Go, CGO_ENABLED=0
set -eu
ARCH=$1 OUT=$2
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
case $ARCH in
amd64) CC=${CC_AMD64:-gcc} ;;
arm64) CC=${CC_ARM64:?set CC_ARM64 to a static-capable aarch64 C compiler} ;;
*) echo "build.sh: ARCH is amd64 or arm64" >&2; exit 2 ;;
esac
$CC -O2 -static -pthread -o "$OUT/core-probe" "$HERE/core-probe.c"
$CC -O2 -static -pthread -o "$OUT/ipc-probe" "$HERE/../../regression/ipc-probe.c"
$CC -O2 -static -o "$OUT/unix-probe" "$HERE/unix-probe.c"
(cd "$HERE/go-probe" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH GOFLAGS=-mod=mod go build -trimpath -ldflags=-s -o "$OUT/go-probe" .)
ls -l "$OUT"
