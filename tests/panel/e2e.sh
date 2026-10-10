#!/bin/sh
# Starts an engine with the two-service Compose fixture and the Web Panel,
# then runs tests/panel/e2e.py in Chromium.
#
#   tests/panel/e2e.sh THOTHDOCK_BINARY GARDEN_PROOT_BINARY SCREENSHOT_DIR
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
SHOTS="$3"
HERE="$(cd "$(dirname "$0")" && pwd)"
FIXTURE="$HERE/../fixtures/compose/two-service"
mkdir -p "$SHOTS"
WORK="$(mktemp -d /tmp/tdpanel.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock" DOCKER_CONFIG="$WORK/cli" WEB_PORT=18092
unset DOCKER_CONTEXT
PID= PANEL=
cleanup() {
    [ -n "$PANEL" ] && kill "$PANEL" 2>/dev/null
    (cd "$FIXTURE" && docker compose -p demo down -v >/dev/null 2>&1) || true
    [ -n "$PID" ] && { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
    case "$WORK" in /tmp/tdpanel.*) rm -rf "$WORK" "$WORK.log" "$WORK.panel" ;; esac
}
trap cleanup EXIT
"$TD" serve --root "$WORK" --proot "$PROOT" > "$WORK.log" 2>&1 &
PID=$!
for _ in $(seq 100); do docker version >/dev/null 2>&1 && break; sleep 0.1; done
(cd "$FIXTURE" && docker compose -p demo up -d >/dev/null 2>&1)
"$TD" panel --root "$WORK" --listen 127.0.0.1:17690 > "$WORK.panel" 2>&1 &
PANEL=$!
for _ in $(seq 100); do [ -f "$WORK/panel/pairing.json" ] && break; sleep 0.1; done
sed -n '1,4p' "$WORK.panel"
python3 -I "$HERE/e2e.py" https://127.0.0.1:17690/ "$WORK/panel/pairing.json" "$SHOTS"
st=$?
kill "$PANEL"; wait "$PANEL" 2>/dev/null || true; PANEL=
ss -Hltn | awk '{print $4}' | grep -qx '127.0.0.1:17690' && { echo "FAIL: the panel still listens after it stopped"; st=1; } || echo "PASS: stopping the panel frees its listener"
[ -f "$WORK/panel/pairing.json" ] && { echo "FAIL: pairing.json left behind"; st=1; } || echo "PASS: the pairing code file is removed on exit"
exit $st
