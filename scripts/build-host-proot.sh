#!/bin/sh
# Build PRoot for this Linux host, for ThothDock's runtime tests and smoke
# test. It is termux/proot at the commit Garden pins (tag v5.1.107.92), the
# same PRoot family ThothTerm ships on Android; Garden's own patches are not
# needed for what the tests exercise.
#
#   scripts/build-host-proot.sh OUT_DIR      -> OUT_DIR/proot
#
# Needs git, make, a C compiler and talloc headers (Debian/Ubuntu:
# libtalloc-dev).
set -eu
OUT="${1:?usage: build-host-proot.sh OUT_DIR}"
COMMIT=7266fb3e8516535682f5a9c8f3a7e70f6506eddb
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
SRC="$OUT/proot-src"
if [ -x "$OUT/proot" ] && [ "$(cat "$OUT/proot.commit" 2>/dev/null)" = "$COMMIT" ]; then
    echo "build-host-proot: $OUT/proot is up to date"
    exit 0
fi
rm -rf "$SRC"
git init -q "$SRC"
git -C "$SRC" fetch -q --depth 1 https://github.com/termux/proot.git "$COMMIT"
git -C "$SRC" checkout -q FETCH_HEAD
[ "$(git -C "$SRC" rev-parse HEAD)" = "$COMMIT" ] || { echo "build-host-proot: wrong commit" >&2; exit 1; }
make -s -C "$SRC/src" proot CFLAGS="-O2 -Wno-implicit-function-declaration" >"$OUT/build.log" 2>&1 \
    || { tail -30 "$OUT/build.log" >&2; exit 1; }
cp "$SRC/src/proot" "$OUT/proot"
echo "$COMMIT" > "$OUT/proot.commit"
"$OUT/proot" --help | grep -q -- --kill-on-exit
echo "build-host-proot: built $OUT/proot ($COMMIT)"
