#!/bin/sh
# Smoke test: start ThothDock on a private Unix socket and drive it with an
# unmodified Docker CLI. Fails unless every answer comes from ThothDock.
#
#   tests/smoke/docker-cli-smoke.sh THOTHDOCK_BINARY PROOT_BINARY
#
# SMOKE_PULL=1 also pulls a small image from Docker Hub and runs it.
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
WORK="$(mktemp -d /tmp/tds.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
unset DOCKER_CONTEXT
"$TD" serve --root "$WORK" --proot "$PROOT" > "$WORK.log" 2>&1 &
PID=$!
cleanup() {
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    case "$WORK" in /tmp/tds.*) rm -rf "$WORK" ;; esac
}
trap cleanup EXIT
for _ in $(seq 50); do [ -S "$WORK/run/thothdock.sock" ] && break; sleep 0.1; done
fail() { echo "FAIL: $*"; echo "--- daemon log"; cat "$WORK.log"; exit 1; }
pass() { echo "PASS: $*"; }

[ "$(stat -c %a "$WORK/run/thothdock.sock")" = 600 ] || fail "socket is not 0600"
pass "socket $WORK/run/thothdock.sock is 0600"
docker version >"$WORK/version.txt" 2>&1 || fail "docker version: $(cat "$WORK/version.txt")"
grep -q "Server: ThothDock" "$WORK/version.txt" || fail "server is not ThothDock: $(cat "$WORK/version.txt")"
grep -q "API version:.*1.41" "$WORK/version.txt" || fail "API was not negotiated to 1.41"
pass "docker version (server ThothDock, API 1.41)"
[ "$(docker info --format '{{.Driver}} {{.CgroupDriver}}')" = "thothdock-copy none" ] || fail "docker info"
pass "docker info"
docker ps >/dev/null || fail "docker ps"
pass "docker ps"
docker images >/dev/null || fail "docker images"
pass "docker images"
if docker network ls >/dev/null 2>"$WORK/net.err"; then fail "networks should be unsupported"; fi
grep -q "ThothDock does not implement" "$WORK/net.err" || fail "unsupported endpoint message: $(cat "$WORK/net.err")"
pass "unsupported endpoints say so"

if [ "${SMOKE_PULL:-0}" = 1 ]; then
    docker pull -q alpine:3.20 >/dev/null || fail "docker pull"
    pass "docker pull alpine:3.20"
    out="$(docker run --rm alpine:3.20 echo smoke-ok)" || fail "docker run"
    [ "$out" = smoke-ok ] || fail "docker run printed '$out'"
    pass "docker run --rm alpine echo"
    set +e
    docker run --name exit3 alpine:3.20 sh -c 'echo to-stderr >&2; exit 3' 2>/dev/null
    code=$?
    set -e
    [ "$code" = 3 ] || fail "exit code $code, want 3"
    [ "$(docker logs exit3 2>&1)" = to-stderr ] || fail "logs"
    docker container prune -f >/dev/null
    pass "exit codes, logs, prune"
    id="$(docker run -d alpine:3.20 sleep 60)"
    docker stop -t 1 "$id" >/dev/null
    [ "$(docker inspect -f '{{.State.Status}}' "$id")" = exited ] || fail "stop"
    docker rm "$id" >/dev/null
    pass "run -d, stop, rm"
fi
echo "smoke: all checks passed"
