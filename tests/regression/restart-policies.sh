#!/bin/sh
# Restart policies, events and docker update with the stock Docker CLI and a
# real PRoot (tickets P1-02, P3-01..P3-03). The daemon is killed with SIGKILL,
# as Android kills an app process, and stopped with SIGTERM, as Exit does.
#
#   tests/regression/restart-policies.sh THOTHDOCK_BINARY PROOT_BINARY
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
IMAGE="${TEST_IMAGE:-alpine:3.20}"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
WORK="$(mktemp -d /tmp/tdrst.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
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
# proot processes serving this data root (containers), by exact argument.
orphans() { ps -eo args | grep -c "^$PROOT --rootfs=$WORK/" || true; }
cleanup() {
    [ -n "$PID" ] && { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
    case "$WORK" in /tmp/tdrst.*) rm -rf "$WORK" "$WORK.log" ;; esac
}
trap cleanup EXIT
state() { docker inspect -f '{{.State.Status}}' "$1"; }
wait_state() { # NAME WANT: up to 10 s
    for _ in $(seq 100); do [ "$(state "$1")" = "$2" ] && return 0; sleep 0.1; done
    return 1
}

start_daemon
docker pull -q "$IMAGE" >/dev/null
T0=$(date +%s)

# on-failure:2 retries twice, then stays exited with the workload's code.
docker run -d --name crash --restart on-failure:2 "$IMAGE" sh -c 'exit 3' >/dev/null
for _ in $(seq 50); do
    [ "$(docker inspect -f '{{.RestartCount}} {{.State.Status}}' crash)" = "2 exited" ] && break; sleep 0.1
done
check "on-failure:2 restarts twice and stays exited" "$(docker inspect -f '{{.RestartCount}} {{.State.Status}} {{.State.ExitCode}}' crash)" "2 exited 3"

docker run -d --name svc --restart unless-stopped "$IMAGE" sleep 100000 >/dev/null
docker run -d --name alw --restart always "$IMAGE" sleep 100000 >/dev/null
docker run -d --name plain "$IMAGE" sleep 100000 >/dev/null
docker run -d --name stopped --restart unless-stopped "$IMAGE" sleep 100000 >/dev/null
docker stop -t 1 stopped alw >/dev/null
sleep 1
check "a manual stop is final while the daemon runs (unless-stopped)" "$(state stopped)" exited
check "a manual stop is final while the daemon runs (always)" "$(state alw)" exited

# docker events replays this run's history for one container.
ev=$(docker events --since "$T0" --until "$(($(date +%s) + 1))" --filter container=svc --format '{{.Action}}' | tr '\n' ' ')
check "docker events for svc" "$ev" "create start "

# Android kills the app: SIGKILL the daemon. Containers die with it.
kill -9 "$PID"; wait "$PID" 2>/dev/null || true; PID=
sleep 1
check "no container process outlives a SIGKILLed daemon" "$(orphans)" 0
start_daemon
wait_state svc running && pass "unless-stopped restored after the daemon was killed" || fail "svc is $(state svc)"
wait_state alw running && pass "always restored at daemon start even after a manual stop (dockerd semantics)" || fail "alw is $(state alw)"
check "a manually stopped unless-stopped container stays stopped" "$(state stopped)" exited
check "a container without a policy is not restored" "$(state plain)" exited
check "its exit is recorded as 137" "$(docker inspect -f '{{.State.ExitCode}}' plain)" 137
check "an exhausted on-failure container stays exited" "$(state crash)" exited

# docker update --restart takes effect for the next daemon start.
docker update --restart always plain >/dev/null
check "docker update --restart" "$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' plain)" always
docker update --memory 64m plain >/dev/null 2>&1 && fail "docker update --memory was accepted" || pass "docker update --memory is refused"

# Exit in the app: SIGTERM. A shutdown stop is not a manual stop.
kill -TERM "$PID"; wait "$PID" 2>/dev/null || true; PID=
check "no container process outlives a stopped daemon" "$(orphans)" 0
start_daemon
wait_state svc running && pass "unless-stopped restored after a clean daemon stop" || fail "svc is $(state svc)"
wait_state plain running && pass "the updated policy restores 'plain'" || fail "plain is $(state plain)"

# A crash loop backs off: in 4 s, 100+200+400+800+1600 ms allows at most 5 restarts.
docker run -d --name loop --restart always "$IMAGE" false >/dev/null
sleep 4
n=$(docker inspect -f '{{.RestartCount}}' loop)
if [ "$n" -ge 3 ] && [ "$n" -le 6 ]; then pass "crash loop backs off ($n restarts in 4 s)"; else fail "crash loop restarted $n times in 4 s"; fi
docker ps --filter name=loop --format '{{.Status}}' | grep -q "^Restarting\|^Up" && pass "docker ps lists a restarting container" || fail "docker ps: $(docker ps -a --filter name=loop --format '{{.Status}}')"
docker rm -f loop >/dev/null && pass "rm -f of a crash-looping container"
sleep 1
check "the removed loop does not come back" "$(docker ps -aq --filter name=loop | wc -l)" 0

docker rm -f svc alw plain stopped crash >/dev/null
check "no container process left at the end" "$(orphans)" 0
echo "restart-policies: $fails failure(s)"
[ "$fails" -eq 0 ]
