#!/bin/sh
# Lifecycle regression against a real PRoot and a real image, driven by the
# stock Docker CLI: run, start, stop, restart, kill, wait, logs, rm, missing
# command (127), an interactive TTY, and the pull ceilings.
#
#   tests/regression/lifecycle.sh THOTHDOCK_BINARY PROOT_BINARY
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
WORK="$(mktemp -d /tmp/tdl.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
unset DOCKER_CONTEXT
start_daemon() { "$TD" serve --root "$WORK" --proot "$PROOT" "$@" > "$WORK.log" 2>&1 & PID=$!
    for _ in $(seq 50); do [ -S "$WORK/run/thothdock.sock" ] && return; sleep 0.1; done; }
stop_daemon() { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
trap 'stop_daemon; case "$WORK" in /tmp/tdl.*) rm -rf "$WORK" "$WORK.log" ;; esac' EXIT
fail() { echo "FAIL: $*"; cat "$WORK.log"; exit 1; }
pass() { echo "PASS: $*"; }
state() { docker inspect -f '{{.State.Status}}' "$1"; }

start_daemon
docker pull -q alpine:3.20 >/dev/null || fail pull
pass "pull"
id="$(docker run -d --name lc alpine:3.20 sh -c 'echo started; sleep 300')"
sleep 1
[ "$(state lc)" = running ] || fail "run -d"
docker logs lc 2>&1 | grep -q '^started$' || fail "logs"
pass "run -d, logs"
docker stop -t 2 lc >/dev/null; [ "$(state lc)" = exited ] || fail "stop"
[ "$(docker inspect -f '{{.State.ExitCode}}' lc)" = 143 ] || fail "stop exit code"
pass "stop (exit 143)"
docker start lc >/dev/null; sleep 1; [ "$(state lc)" = running ] || fail "start"
pass "start"
docker restart -t 2 lc >/dev/null; sleep 1; [ "$(state lc)" = running ] || fail "restart"
pass "restart"
docker kill lc >/dev/null; sleep 1; [ "$(state lc)" = exited ] || fail "kill"
[ "$(docker inspect -f '{{.State.ExitCode}}' lc)" = 137 ] || fail "kill exit code"
pass "kill (exit 137)"
docker start lc >/dev/null; docker kill -s TERM lc >/dev/null
[ "$(docker wait lc)" = 143 ] || fail "wait after TERM"
pass "kill -s TERM, wait (143)"
docker rm lc >/dev/null; docker inspect lc >/dev/null 2>&1 && fail "rm"
pass "rm"
c=0; docker run --rm alpine:3.20 /no/such/command >/dev/null 2>&1 || c=$?
[ "$c" = 127 ] || [ "$c" = 126 ] || fail "missing command exited $c"
c=0; docker run --rm alpine:3.20 nosuchcmd >/dev/null 2>&1 || c=$?
[ "$c" = 127 ] || fail "missing command in PATH exited $c, want 127"
pass "missing command (127)"
# Interactive TTY: a pseudo-terminal in, a command out.
out="$(printf 'echo tty-$((6*7))\nexit\n' | script -qec 'docker run -it --rm alpine:3.20 sh' /dev/null | tr -d '\r')"
echo "$out" | grep -q 'tty-42' || fail "interactive TTY: $out"
pass "interactive TTY (docker run -it)"
# A process that exits on its own while others are running does not disturb them.
docker run -d --name neighbour alpine:3.20 sleep 120 >/dev/null
docker run --rm alpine:3.20 true
[ "$(state neighbour)" = running ] || fail "a neighbour's exit disturbed a running container"
docker rm -f neighbour >/dev/null
pass "neighbour isolation"
# Pull ceilings, from the command line.
stop_daemon
start_daemon --max-extracted-bytes 1000000
if docker pull -q debian:trixie-slim >/dev/null 2>"$WORK/err"; then fail "a 1 MB ceiling let debian through"; fi
grep -q 'extracted size limit' "$WORK/err" || fail "limit message: $(cat "$WORK/err")"
[ -z "$(ls -d "$WORK"/tmp/rootfs-* 2>/dev/null)" ] || fail "partial extraction left in tmp: $(ls "$WORK/tmp")"
pass "pull ceiling (--max-extracted-bytes): clear error, nothing left behind"
echo "lifecycle: all checks passed"
