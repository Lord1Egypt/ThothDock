#!/bin/sh
# Resource baseline (docs/nextgen/PERFORMANCE_BASELINE.md, ticket P0-03).
# Starts ThothDock on a private data root and measures it with the stock
# Docker CLI. Everything is read from /proc, so it needs no profiler.
#
#   tests/perf/baseline.sh THOTHDOCK_BINARY PROOT_BINARY [OUT_FILE]
#
# PERF_IMAGE (default alpine:3.20) is pulled once; PERF_WINDOW (default 10)
# is the length in seconds of each idle sampling window; PERF_REPS (default 3)
# is how many windows and timed runs are taken. PERF_NETWORK=1 runs the four
# idle containers on a user-defined network (needs a PRoot with --net-ip).
#
# Output is key=value lines. Every figure is a raw measurement of this run on
# this machine; compare runs only when the machine and load are the same.
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
OUT="${3:-/dev/stdout}"
IMAGE="${PERF_IMAGE:-alpine:3.20}"
WINDOW="${PERF_WINDOW:-10}"
REPS="${PERF_REPS:-3}"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
WORK="$(mktemp -d /tmp/tdperf.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
unset DOCKER_CONTEXT
PID=
cleanup() {
    [ -n "$PID" ] && { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
    case "$WORK" in /tmp/tdperf.*) rm -rf "$WORK" ;; esac
}
trap cleanup EXIT
CLK=$(getconf CLK_TCK)

# A monotonic clock: WSL2 and phones step the wall clock, which turns a
# date-based interval negative.
if command -v python3 >/dev/null; then
    now_ns() { python3 -I -c 'import time; print(time.monotonic_ns())'; }
else
    now_ns() { date +%s%N; }
fi
ms_since() { echo $(( ($(now_ns) - $1) / 1000000 )); }
emit() { echo "$1=$2" >> "$WORK/result"; }

# cpu_ticks PID: utime+stime of the process (fields 14 and 15 of stat;
# the command name in field 2 never contains a space for these binaries).
cpu_ticks() { awk '{print $14 + $15}' "/proc/$1/stat"; }
# switches PID: voluntary + involuntary context switches of every thread of
# the process (the Go runtime's threads are separate tasks) = wakeups.
switches() { cat /proc/"$1"/task/*/status 2>/dev/null | awk '/^(voluntary|nonvoluntary)_ctxt_switches/ {s += $2} END {print s + 0}'; }
rss_kib() { awk '/^VmRSS/ {print $2}' "/proc/$1/status"; }
threads() { awk '/^Threads/ {print $2}' "/proc/$1/status"; }
# descendants PID: every process below PID (proot and its tracees).
descendants() {
    for c in $(cat /proc/"$1"/task/*/children 2>/dev/null); do
        echo "$c"; descendants "$c"
    done
}
tree_sum() { # FUNC PID: FUNC summed over PID and its descendants
    t=0
    for p in "$2" $(descendants "$2"); do
        v=$("$1" "$p" 2>/dev/null || echo 0); t=$((t + ${v:-0}))
    done
    echo "$t"
}
tree_rss_kib() {
    t=0
    for p in "$1" $(descendants "$1"); do
        r=$(rss_kib "$p" 2>/dev/null || echo 0); t=$((t + ${r:-0}))
    done
    echo "$t"
}

# idle_window LABEL: the daemon's CPU time (ms, clock-tick resolution) and
# context switches (wakeups) in each WINDOW-second window, then its RSS and
# threads, and the RSS of the whole process tree.
idle_window() {
    i=1
    while [ "$i" -le "$REPS" ]; do
        t0=$(cpu_ticks "$PID"); s0=$(switches "$PID")
        T0=$(tree_sum cpu_ticks "$PID"); S0=$(tree_sum switches "$PID")
        sleep "$WINDOW"
        t1=$(cpu_ticks "$PID"); s1=$(switches "$PID")
        T1=$(tree_sum cpu_ticks "$PID"); S1=$(tree_sum switches "$PID")
        emit "$1.window$i.daemon_cpu_ms" $(( (t1 - t0) * 1000 / CLK ))
        emit "$1.window$i.daemon_wakeups" $((s1 - s0))
        emit "$1.window$i.tree_cpu_ms" $(( (T1 - T0) * 1000 / CLK ))
        emit "$1.window$i.tree_wakeups" $((S1 - S0))
        i=$((i + 1))
    done
    emit "$1.daemon_rss_kib" "$(rss_kib "$PID")"
    emit "$1.daemon_threads" "$(threads "$PID")"
    emit "$1.tree_rss_kib" "$(tree_rss_kib "$PID")"
    emit "$1.tree_processes" $(( $(descendants "$PID" | wc -l) + 1 ))
}

# timed LABEL CMD...: wall time of CMD, REPS times.
timed() {
    label=$1; shift
    i=1
    while [ "$i" -le "$REPS" ]; do
        t=$(now_ns); "$@" >/dev/null 2>&1 || { echo "FAIL: $label: $*" >&2; exit 1; }
        emit "$label.run$i.ms" "$(ms_since "$t")"
        i=$((i + 1))
    done
}

{
    echo "# ThothDock resource baseline"
    echo "date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "thothdock=$("$TD" version 2>/dev/null | head -1)"
    echo "kernel=$(uname -r)"
    echo "cpu=$(awk -F': ' '/^model name|^Hardware/ {print $2; exit}' /proc/cpuinfo)"
    echo "cpus=$(getconf _NPROCESSORS_ONLN)"
    echo "mem_total_kib=$(awk '/^MemTotal/ {print $2}' /proc/meminfo)"
    echo "loadavg_start=$(cut -d' ' -f1-3 /proc/loadavg)"
    echo "image=$IMAGE window_s=$WINDOW reps=$REPS network=${PERF_NETWORK:-0}"
} > "$WORK/result"

# The clock's own cost (process start of the helper), included once in every
# interval below; subtract it when comparing very short operations.
t=$(now_ns); emit clock_overhead_ms "$(ms_since "$t")"

# 1. Engine startup: exec to the first successful /_ping.
t=$(now_ns)
"$TD" serve --root "$WORK" --proot "$PROOT" > "$WORK/daemon.log" 2>&1 &
PID=$!
until docker version >/dev/null 2>&1; do
    kill -0 "$PID" 2>/dev/null || { cat "$WORK/daemon.log" >&2; exit 1; }
    sleep 0.01
done
emit startup_to_ready_ms "$(ms_since "$t")"

# 2. Idle daemon, no containers.
idle_window idle_empty

# 3. Image pull (network-dependent; one pull, timed once).
t=$(now_ns)
docker pull -q "$IMAGE" >/dev/null
emit pull_ms "$(ms_since "$t")"

# 4. One-container start-to-exit latency and exec latency.
timed run_rm_true docker run --rm "$IMAGE" true
docker run -d --name perf-exec "$IMAGE" sleep 100000 >/dev/null
timed exec_true docker exec perf-exec true
docker rm -f perf-exec >/dev/null

# 5. Four idle containers.
NETOPT=
if [ "${PERF_NETWORK:-0}" = 1 ]; then
    docker network create perfnet >/dev/null
    NETOPT="--network perfnet"
fi
for n in 1 2 3 4; do docker run -d --name "perf-idle$n" $NETOPT "$IMAGE" sleep 100000 >/dev/null; done
sleep 1
idle_window idle_four

# 6. Filesystem write inside a container (64 MiB with fsync).
t=$(now_ns)
docker exec perf-idle1 dd if=/dev/zero of=/tmp/perf.bin bs=1048576 count=64 conv=fsync >/dev/null 2>&1
emit fs_write_64mib_ms "$(ms_since "$t")"
t=$(now_ns)
docker exec perf-idle1 sh -c 'cat /tmp/perf.bin > /dev/null'
emit fs_read_64mib_ms "$(ms_since "$t")"
for n in 1 2 3 4; do docker rm -f "perf-idle$n" >/dev/null; done

# 7. TCP: 64 MiB from a host client through the -p forwarder into a
# container, then 64 MiB container to container over loopback. The receiver
# exits at end of stream, so `docker wait` marks the end of the transfer.
tcp_sink() { # NAME PUBLISH
    docker run -d --name "$1" $2 "$IMAGE" sh -c 'nc -l -p 8000 > /dev/null' >/dev/null
    sleep 1
}
if command -v python3 >/dev/null; then
    tcp_sink perf-tcp "-p 127.0.0.1:18765:8000"
    t=$(now_ns)
    python3 -I -c 'import socket
s = socket.create_connection(("127.0.0.1", 18765))
b = bytes(1 << 20)
for _ in range(64): s.sendall(b)
s.shutdown(socket.SHUT_WR)
s.recv(1)'
    docker wait perf-tcp >/dev/null
    emit tcp_64mib_host_to_container_via_forwarder_ms "$(ms_since "$t")"
    docker rm -f perf-tcp >/dev/null
fi
tcp_sink perf-tcp ""
docker run -d --name perf-client "$IMAGE" sleep 100000 >/dev/null
t=$(now_ns)
docker exec perf-client sh -c 'head -c 67108864 /dev/zero | nc 127.0.0.1 8000' >/dev/null 2>&1 &
CLIENT=$!
docker wait perf-tcp >/dev/null
emit tcp_64mib_container_to_container_ms "$(ms_since "$t")"
wait "$CLIENT" || true
docker rm -f perf-tcp perf-client >/dev/null

# 8. Idle again after the workload: nothing should be left running.
sleep 1
idle_window idle_after
emit loadavg_end "$(cut -d' ' -f1-3 /proc/loadavg)"

cat "$WORK/result" > "$OUT"
