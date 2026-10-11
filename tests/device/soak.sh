#!/bin/bash
# Long-running workload soak on a phone, sampled from a computer over adb (read only).
#
#   tests/device/soak.sh PACKAGE MINUTES OUT.csv [API_PORT EXPLORER_PORT]
#
# Every minute: engine pid (a change is a restart), RSS and threads, how many
# containers run, whether the TON API (/block/latest JSON) and the explorer
# (HTTP 200) answer through adb forwards, how many processes the app has,
# Android's phantom-process kills so far, battery level and temperature.
# Set ADB="adb -s SERIAL". It never touches the workloads.
set -u
PKG=$1 MIN=$2 OUT=$3 API=${4:-4000} EXP=${5:-8080}
ADB=${ADB:-adb}
$ADB forward tcp:24000 tcp:$API >/dev/null && $ADB forward tcp:28080 tcp:$EXP >/dev/null
UID_=$($ADB shell "ps -o USER -p \$(pidof $PKG)" 2>/dev/null | tail -1 | tr -d '\r')
$ADB logcat -c
echo "time,minute,engine_pid,engine_rss_kb,engine_threads,app_alive,app_procs,running,api_ok,api_seqno,explorer_http,phantom_kills,battery,temp_c" > "$OUT"
for m in $(seq 0 "$MIN"); do
    t=$(date +%s)
    ep=$($ADB shell 'for p in $(ls /proc | grep -E "^[0-9]+$"); do c=$(tr "\0" " " </proc/$p/cmdline 2>/dev/null); case "$c" in *libthothdock.so\ serve*) echo $p;; esac; done' 2>/dev/null | head -1 | tr -d '\r')
    rss=""; thr=""
    [ -n "$ep" ] && read -r rss thr <<<"$($ADB shell "awk '/VmRSS/{r=\$2} /Threads/{t=\$2} END{print r, t}' /proc/$ep/status" 2>/dev/null | tr -d '\r')"
    alive=$($ADB shell pidof "$PKG" >/dev/null 2>&1 && echo 1 || echo 0)
    procs=$($ADB shell 'ps -A -o USER' 2>/dev/null | grep -c "^$UID_")
    # the engine's own answer, over its socket (no guest process, no extra phantom process)
    running=$($ADB shell "run-as $PKG sh -c 'printf \"GET /containers/json HTTP/1.0\\r\\nHost: x\\r\\n\\r\\n\" | nc -U files/thothdock/sock/thothdock.sock'" 2>/dev/null | tail -1 | python3 -I -c 'import json,sys
try: print(sum(1 for c in json.loads(sys.stdin.read()) if c.get("State") == "running"))
except Exception: print(0)')
    body=$(curl -s -m 10 http://127.0.0.1:24000/block/latest 2>/dev/null)
    seq=$(printf '%s' "$body" | python3 -I -c 'import json,sys
try: print(json.load(sys.stdin)["last"]["seqno"])
except Exception: print("")' 2>/dev/null)
    apiok=$([ -n "$seq" ] && echo 1 || echo 0)
    exp=$(curl -s -m 10 -o /dev/null -w '%{http_code}' http://127.0.0.1:28080/ 2>/dev/null)
    kills=$($ADB logcat -d 2>/dev/null | grep -c "Killing PhantomProcessRecord.*u0a[0-9]*}: Trimming" )
    bat=$($ADB shell dumpsys battery 2>/dev/null | awk -F': ' '/^  level: /{print $2; exit}' | tr -d '\r')
    tmp=$($ADB shell dumpsys battery 2>/dev/null | awk -F': ' '/^  temperature: /{printf "%.1f", $2/10; exit}' | tr -d '\r')
    echo "$(date -u +%FT%TZ),$m,$ep,$rss,$thr,$alive,$procs,$running,$apiok,$seq,$exp,$kills,$bat,$tmp" >> "$OUT"
    [ "$m" -lt "$MIN" ] && sleep $(( 60 - ($(date +%s) - t) > 0 ? 60 - ($(date +%s) - t) : 1 ))
done
echo "soak done: $OUT"
