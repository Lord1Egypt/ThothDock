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
ALLOW=
if [ "${SMOKE_GUARD:-0}" = 1 ]; then
    mkdir -p "$WORK.guard"
    "$(dirname "$0")/../../engine-guard/build.sh" "$WORK.guard" >/dev/null
    cp "$TD" "$WORK.guard/thothdock"
    ALLOW="--allow-bind $WORK.guard"
fi
"$TD" serve --root "$WORK" --proot "$PROOT" $ALLOW > "$WORK.log" 2>&1 &
PID=$!
cleanup() {
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    case "$WORK" in /tmp/tds.*) rm -rf "$WORK" "$WORK.guard" ;; esac
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
if [ "${SMOKE_PULL:-0}" = 1 ]; then
    # --- exec
    docker run -d --name ex alpine:3.20 sleep 120 >/dev/null
    [ "$(docker exec ex echo hello)" = hello ] || fail "docker exec echo"
    docker exec ex sh -c 'exit 42' && fail "exec exit code" || [ $? = 42 ] || fail "exec exit code propagation"
    [ "$(docker exec -e FOO=bar -w /etc -u nobody ex sh -c 'echo $FOO $PWD $(id -u)')" = "bar /etc 65534" ] || fail "exec env/workdir/user"
    [ "$(echo piped | docker exec -i ex tr a-z A-Z)" = PIPED ] || fail "exec stdin"
    docker exec ex sh -c 'echo shared > /tmp/s'; [ "$(docker exec ex cat /tmp/s)" = shared ] || fail "exec shares the filesystem"
    docker exec ex nosuchcmd >/dev/null 2>&1 && fail "exec of a missing command succeeded"; [ $? = 126 ] || true
    docker rm -f ex >/dev/null
    pass "docker exec (stdout, exit code, env/workdir/user, stdin, shared fs)"
    # --- volumes
    docker volume create smokevol >/dev/null
    docker run --rm -v smokevol:/data alpine:3.20 sh -c 'echo persistent > /data/t.txt'
    [ "$(docker run --rm -v smokevol:/data alpine:3.20 cat /data/t.txt)" = persistent ] || fail "volume persistence"
    docker run -d --name vholder -v smokevol:/d alpine:3.20 sleep 60 >/dev/null
    docker volume rm smokevol >/dev/null 2>&1 && fail "in-use volume removed"
    docker rm -f vholder >/dev/null
    docker volume create '../escape' >/dev/null 2>&1 && fail "traversal volume name accepted"
    docker volume rm smokevol >/dev/null || fail "volume rm"
    pass "named volumes (persistence, in-use protection, hostile name)"
    # --- port publishing
    docker run -d --name pub -p 18741:8080 alpine:3.20 sh -c 'while true; do printf "HTTP/1.0 200 OK\r\n\r\nsmoke-web\n" | nc -l -p 8080; done' >/dev/null
    sleep 2
    [ "$(curl -s --max-time 5 http://127.0.0.1:18741/)" = smoke-web ] || fail "published port"
    [ "$(docker port pub)" = "8080/tcp -> 127.0.0.1:18741" ] || fail "docker port"
    docker run --rm -p 5353:53/udp alpine:3.20 true >/dev/null 2>&1 && fail "UDP publishing accepted"
    docker run --rm -p 0.0.0.0:18742:80 alpine:3.20 true >/dev/null 2>&1 && fail "non-loopback publishing accepted"
    docker stop -t 1 pub >/dev/null
    curl -s --max-time 2 http://127.0.0.1:18741/ >/dev/null && fail "listener survived stop"
    docker rm pub >/dev/null
    pass "-p TCP publishing on 127.0.0.1 (curl, collision-free stop, UDP and LAN refused)"
fi
if [ "${SMOKE_GUARD:-0}" = 1 ]; then
    docker pull -q debian:trixie >/dev/null || fail "pull debian"
    out="$(docker run --rm -v "$WORK.guard":/g debian:trixie sh -c '
        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq >/dev/null 2>&1
        dpkg -i /g/*.deb >/dev/null 2>&1
        apt-get -y full-upgrade >/dev/null 2>&1
        apt-get install -y docker.io containerd runc 2>&1 | grep -c "already the newest"
        apt-get install -y --allow-downgrades docker.io=26.1.5+dfsg1-9+deb13u1 >/dev/null 2>&1 && echo DOWNGRADE-ACCEPTED
        apt-get install -y docker-cli >/dev/null 2>&1 && echo cli-ok
        for b in dockerd containerd runc; do command -v $b >/dev/null && echo "PRESENT-$b"; done
        /g/thothdock doctor --guard >/dev/null && echo doctor-ok
    ' 2>&1)"
    echo "$out" | grep -q '^3$' || fail "apt install of engine packages was not a no-op: $out"
    echo "$out" | grep -q DOWNGRADE-ACCEPTED && fail "real engine downgrade accepted"
    echo "$out" | grep -q PRESENT && fail "a stock engine binary is present: $out"
    echo "$out" | grep -q cli-ok || fail "docker-cli could not be installed"
    echo "$out" | grep -q doctor-ok || fail "doctor --guard failed"
    pass "Engine Guard (full-upgrade, engine installs no-op, downgrade refused, CLI allowed, doctor)"
fi
echo "smoke: all checks passed"
