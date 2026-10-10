#!/bin/sh
# Docker Compose against ThothDock (tickets P4-03..P4-05): the MVP commands on
# tests/fixtures/compose/two-service with a real PRoot that has --net-ip.
#
#   tests/regression/compose.sh THOTHDOCK_BINARY GARDEN_PROOT_BINARY [COMPOSE...]
#
# COMPOSE defaults to "docker compose"; pass e.g. a Debian docker-compose
# binary to test another build. Needs python3 on the host.
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
shift 2
[ $# -gt 0 ] || set -- docker compose
FIXTURE="$(cd "$(dirname "$0")/../fixtures/compose/two-service" && pwd)"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
"$PROOT" --help | grep -q -- --net-ip || { echo "SKIP: $PROOT has no --net-ip (Garden patch 0009)"; exit 2; }
WORK="$(mktemp -d /tmp/tdcomp.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
export WEB_PORT=18091
unset DOCKER_CONTEXT
PID=
fails=0
pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; fails=$((fails + 1)); }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: got '$2', want '$3'"; fi; }
start_daemon() {
    "$TD" serve --root "$WORK" --proot "$PROOT" >> "$WORK.log" 2>&1 &
    PID=$!
    for _ in $(seq 100); do docker version >/dev/null 2>&1 && return 0; sleep 0.1; done
    echo "daemon did not start"; cat "$WORK.log"; exit 1
}
orphans() { ps -eo args | grep -c "^$PROOT --rootfs=$WORK/" || true; }
cleanup() {
    [ -n "$PID" ] && { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
    case "$WORK" in /tmp/tdcomp.*) rm -rf "$WORK" "$WORK.log" ;; esac
}
trap cleanup EXIT
COMPOSE_CMD=$(printf '%s ' "$@")
cmp() { (cd "$FIXTURE" && $COMPOSE_CMD -p demo "$@"); }
http() {
    for _ in $(seq 40); do
        out=$(python3 -I -c 'import sys, urllib.request; print(urllib.request.urlopen(sys.argv[1], timeout=3).read().decode().strip())' "$1" 2>/dev/null || true)
        [ -n "$out" ] && [ "$out" != "web sees api-unreachable" ] && { echo "$out"; return; }
        sleep 0.25
    done
    echo "$out"
}
states() { docker ps -a --filter label=com.docker.compose.project=demo --format '{{.Names}}={{.State}}' | sort | tr '\n' ' '; }

start_daemon
echo "compose: $($COMPOSE_CMD version 2>&1 | head -1)"
cmp config -q && pass "compose config" || fail "compose config"
cmp pull -q >/dev/null 2>&1 && pass "compose pull" || fail "compose pull"
docker run -d --name bystander alpine:3.20 sleep 100000 >/dev/null

cmp up -d >"$WORK/up.log" 2>&1 && pass "compose up -d" || { fail "compose up -d"; cat "$WORK/up.log"; }
check "both services run" "$(states)" "demo-api-1=running demo-web-1=running "
check "web reaches api by service name, published on loopback" "$(http "http://127.0.0.1:$WEB_PORT/")" "web sees api-ok"
check "compose ps" "$(cmp ps --format '{{.Service}}' | sort | tr '\n' ' ')" "api web "
check "compose exec" "$(cmp exec -T web wget -q -O- http://api:3000/)" api-ok
cmp logs api 2>&1 | grep -q "GET / HTTP" && pass "compose logs" || fail "compose logs: $(cmp logs api 2>&1 | tail -3)"
ids=$(docker ps -q --no-trunc | sort | tr '\n' ' ')
cmp up -d >/dev/null 2>&1
check "a second up -d recreates nothing" "$(docker ps -q --no-trunc | sort | tr '\n' ' ')" "$ids"

cmp stop >/dev/null 2>&1
check "compose stop" "$(states)" "demo-api-1=exited demo-web-1=exited "
cmp start >/dev/null 2>&1
check "compose start" "$(states)" "demo-api-1=running demo-web-1=running "
cmp restart >/dev/null 2>&1
check "compose restart" "$(states)" "demo-api-1=running demo-web-1=running "
check "the stack serves after restart" "$(http "http://127.0.0.1:$WEB_PORT/")" "web sees api-ok"

# restart: unless-stopped survives a killed daemon; web has no policy.
kill -9 "$PID"; wait "$PID" 2>/dev/null || true; PID=
sleep 1
check "no container process outlives the killed daemon" "$(orphans)" 0
start_daemon
for _ in $(seq 50); do [ "$(docker inspect -f '{{.State.Status}}' demo-api-1)" = running ] && break; sleep 0.1; done
check "restart: unless-stopped restored the api service" "$(states)" "demo-api-1=running demo-web-1=exited "
cmp up -d >/dev/null 2>&1
check "up -d brings the rest back" "$(http "http://127.0.0.1:$WEB_PORT/")" "web sees api-ok"
check "the named volume kept its data across runs" "$(docker exec demo-api-1 sh -c 'wc -l < /data/boots' | tr -d ' ')" 4

cmp down >"$WORK/down.log" 2>&1 && pass "compose down" || { fail "compose down"; cat "$WORK/down.log"; }
check "down removed the project's containers only" "$(docker ps -a --format '{{.Names}}' | tr '\n' ' ')" "bystander "
check "down removed the project network" "$(docker network ls --format '{{.Name}}' | sort | tr '\n' ' ')" "bridge host none "
check "down kept the named volume" "$(docker volume ls -q | tr '\n' ' ')" "demo_apidata "

# Attached up: logs stream, no stale exit is reported, SIGTERM stops gracefully.
(cd "$FIXTURE" && exec timeout -s TERM 12 $COMPOSE_CMD -p demo up) > "$WORK/attached.log" 2>&1 &
UP=$!
http "http://127.0.0.1:$WEB_PORT/" >/dev/null
wait "$UP" || true
grep -q "api-1  | GET / HTTP" "$WORK/attached.log" && pass "attached up streams service logs" || fail "attached up logs: $(tail -5 "$WORK/attached.log")"
first_exit=$(grep -n "exited with code" "$WORK/attached.log" | head -1 | cut -d: -f1)
first_stop=$(grep -n "Gracefully Stopping\|Gracefully stopping" "$WORK/attached.log" | head -1 | cut -d: -f1)
if [ -z "$first_exit" ] || { [ -n "$first_stop" ] && [ "$first_exit" -gt "$first_stop" ]; }; then
    pass "no exit is reported before the stack is stopped"
else
    fail "an exit was reported while the stack ran: $(sed -n "${first_exit}p" "$WORK/attached.log")"
fi
check "SIGTERM to attached up stops the stack" "$(states)" "demo-api-1=exited demo-web-1=exited "

cmp down -v >/dev/null 2>&1
check "down -v removes the named volume" "$(docker volume ls -q | tr '\n' ' ')" ""
docker rm -f bystander >/dev/null
check "no container process left" "$(orphans)" 0
echo "compose: $fails failure(s)"
[ "$fails" -eq 0 ]
