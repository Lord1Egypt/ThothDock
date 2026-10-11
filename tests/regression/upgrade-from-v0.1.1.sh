#!/bin/sh
# Upgrade compatibility: a data root written by the published ThothDock v0.1.1
# (image, containers, a named volume with data) must open with the current code
# and keep everything.
#
#   tests/regression/upgrade-from-v0.1.1.sh        (run from the repository root)
set -eu
HERE="$(cd "$(dirname "$0")/../.." && pwd)"
W="$(mktemp -d)"
trap 'git -C "$HERE" worktree remove --force "$W/v011" 2>/dev/null; rm -rf "$W"' EXIT
git -C "$HERE" worktree add -q --detach "$W/v011" v0.1.1
cp "$HERE/tests/regression/upgrade-export_test.go.txt" "$W/v011/internal/engine/export_v011_test.go"
mkdir "$W/root"
(cd "$W/v011" && EXPORT_DIR="$W/root" go test -count=1 -run TestExportV011Root ./internal/engine/)
(cd "$HERE" && THOTHDOCK_V011_ROOT="$W/root" go test -count=1 -v -run TestOpensARealV011DataRoot ./internal/engine/)
