#!/bin/bash
# Stress and address-conflict test for containers, Flatpak sandboxes and snap
# services side by side (docs/APP_STORES.md, owner requirement 2026-11-01).
# Runs inside the ThothTerm terminal of a build with Flatpak and snap.
#
#   bash app-stores-stress.sh [HOLD_SECONDS]      (default 180)
#
# Needs: flatpak with org.freedesktop.Platform//24.08 (python3), the redis
# and mosquitto snaps installed, network for docker pulls. Installs curl and
# jq with apt if missing. Everything it starts, it removes.
set -u
HOLD=${1:-180}
N_WEB=${N_WEB:-3} N_RD=${N_RD:-2} N_FP=${N_FP:-4}
SOCK=/run/thothdock/thothdock.sock
SNAP=${SNAP_CMD:-snap}
pass=0 fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
no() { echo "FAIL $*"; fail=$((fail+1)); }
note() { echo "---- $*"; }
command -v jq >/dev/null && command -v curl >/dev/null || sudo apt-get install -y -q jq curl >/dev/null 2>&1

procs() { ps -e --no-headers | wc -l; }
engine_rss() { ps -eo rss,comm | awk '/libthothdock/ {s+=$1} END {print s+0}'; }
apps_json() { curl -s --unix-socket "$SOCK" http://d/thothdock/apps; }

cleanup() {
    note "teardown"
    for i in $(seq "$N_FP"); do [ -f /tmp/fp$i.pid ] && kill "$(cat /tmp/fp$i.pid)" 2>/dev/null; done
    sudo $SNAP stop redis.server >/dev/null 2>&1; sudo $SNAP stop mosquitto.mosquitto >/dev/null 2>&1
    docker rm -f $(docker ps -aq --filter label=stress=1) >/dev/null 2>&1
    docker network rm stress >/dev/null 2>&1
}
trap cleanup EXIT

sudo $SNAP stop redis.server >/dev/null 2>&1; sudo $SNAP stop mosquitto.mosquitto >/dev/null 2>&1
note "baseline"
P0=$(procs); R0=$(engine_rss)
echo "processes $P0, engine RSS ${R0} KiB"

note "start: $N_WEB web + $N_RD redis containers on network 'stress', 1 published container, $N_FP flatpak web servers, redis + mosquitto snaps"
docker network create stress >/dev/null
docker pull -q busybox >/dev/null; docker pull -q redis:alpine >/dev/null
for i in $(seq "$N_WEB"); do
    docker run -d --label stress=1 --name web$i --network stress busybox \
        sh -c "mkdir -p /srv && echo web$i > /srv/id && exec httpd -f -p 8080 -h /srv" >/dev/null
done
for i in $(seq "$N_RD"); do docker run -d --label stress=1 --name rd$i --network stress redis:alpine >/dev/null; done
# The default network shares the phone's network: its servers bind every
# address, so this one uses a port nothing else holds (see the KNOWN check).
docker run -d --label stress=1 --name pub -p 18080:8090 busybox \
    sh -c "mkdir -p /srv && echo pub > /srv/id && exec httpd -f -p 8090 -h /srv" >/dev/null
for i in $(seq "$N_FP"); do
    mkdir -p /tmp/fp$i && echo fp$i > /tmp/fp$i/id
    flatpak run --filesystem=/tmp --command=python3 org.freedesktop.Platform//24.08 \
        -m http.server 8080 --directory /tmp/fp$i > /tmp/fp$i.log 2>&1 &
    echo $! > /tmp/fp$i.pid
done
sudo $SNAP start redis.server >/dev/null; sudo $SNAP start mosquitto.mosquitto >/dev/null
sleep 15

note "addresses"
declare -A ADDR
for i in $(seq "$N_WEB"); do ADDR[web$i]=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' web$i); done
for i in $(seq "$N_RD"); do ADDR[rd$i]=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' rd$i); done
J=$(apps_json)
mapfile -t FPIPS < <(jq -r '.Sandboxes[] | select(.AppID=="org.freedesktop.Platform") | .NetIP' <<<"$J")
for i in $(seq "$N_FP"); do ADDR[fp$i]=${FPIPS[$((i-1))]:-}; done
ADDR[snap-redis]=$(jq -r '.Sandboxes[] | select(.AppID=="snap:redis.server") | .NetIP' <<<"$J")
ADDR[snap-mosquitto]=$(jq -r '.Sandboxes[] | select(.AppID=="snap:mosquitto.mosquitto") | .NetIP' <<<"$J")
for k in $(printf '%s\n' "${!ADDR[@]}" | sort); do printf '  %-15s %s\n' "$k" "${ADDR[$k]}"; done
empty=0; for k in "${!ADDR[@]}"; do [ -n "${ADDR[$k]}" ] || empty=$((empty+1)); done
[ "$empty" = 0 ] && ok "all ${#ADDR[@]} workloads have an address" || no "$empty workloads without an address"
dups=$(printf '%s\n' "${ADDR[@]}" | sort | uniq -d)
[ -z "$dups" ] && ok "no address is shared (containers, flatpak sandboxes and snaps)" || no "shared addresses: $dups"

redis_cmd() { # redis_cmd IP COMMAND...: the reply's last line
    timeout 5 bash -c 'exec 3<>/dev/tcp/$0/6379; printf "%s\r\n" "$1" >&3; read -t 3 a <&3; case $a in \$*) read -t 3 b <&3; a=$b;; esac; printf %s "$a" | tr -d "\r"' "$1" "$2" 2>/dev/null
}

check_all() {
    local bad=0 got k
    for k in $(printf '%s\n' "${!ADDR[@]}" | grep -E '^web'); do
        got=$(curl -s --max-time 5 "http://${ADDR[$k]}:8080/id")
        [ "$got" = "$k" ] || { echo "    $k at ${ADDR[$k]}:8080 answered '$got'"; bad=$((bad+1)); }
    done
    # The flatpak servers are not listed in start order: each address must
    # answer with a different fpN, and together all of them.
    got=$(for k in $(printf '%s\n' "${!ADDR[@]}" | grep -E '^fp'); do curl -s --max-time 5 "http://${ADDR[$k]}:8080/id"; echo; done | grep -v '^$' | sort | tr '\n' ' ')
    want=$(for i in $(seq "$N_FP"); do echo fp$i; done | sort | tr '\n' ' ')
    [ "$got" = "$want" ] || { echo "    flatpak servers answered '$got', want '$want'"; bad=$((bad+1)); }
    got=$(curl -s --max-time 5 http://127.0.0.1:18080/id)
    [ "$got" = pub ] || { echo "    published container answered '$got' ($(docker ps -a --filter name=^pub$ --format '{{.Status}}'); $(docker logs pub 2>&1 | tail -1))"; bad=$((bad+1)); }
    for k in $(printf '%s\n' "${!ADDR[@]}" | grep -E '^(rd|snap-redis)'); do
        got=$(redis_cmd "${ADDR[$k]}" "GET who")
        [ "$got" = "$k" ] || { echo "    redis $k at ${ADDR[$k]} answered '$got'"; bad=$((bad+1)); }
    done
    timeout 5 bash -c "exec 3<>/dev/tcp/${ADDR[snap-mosquitto]}/1883" 2>/dev/null || { echo "    mosquitto not accepting"; bad=$((bad+1)); }
    return $bad
}

note "identity: every server on port 8080 / 6379 answers as itself"
for k in $(printf '%s\n' "${!ADDR[@]}" | grep -E '^(rd|snap-redis)'); do
    [ "$(redis_cmd "${ADDR[$k]}" "SET who $k")" = "+OK" ] || echo "    SET on $k at ${ADDR[$k]} failed"
done
check_all && ok "7 web servers on :8080, 3 redis on :6379 (separate data), mosquitto on :1883, published :18080" || no "identity checks"

note "KNOWN: a default-network container on a port that an addressed workload holds"
docker run -d --label stress=1 --name clash busybox sh -c "exec httpd -f -p 8080 -h /" >/dev/null; sleep 3
if docker logs clash 2>&1 | grep -q "Address already in use"; then
    echo "KNOWN default-network container cannot bind 0.0.0.0:8080 while 127.77.x.y:8080 is held (Linux refuses a wildcard bind over a specific one)"
else
    echo "INFO default-network container on :8080 started: $(docker ps -a --filter name=^clash$ --format '{{.Status}}')"
fi
docker rm -f clash >/dev/null 2>&1

note "hold ${HOLD}s"
PMAX=0; RMAX=0; holdbad=0
for t in $(seq 30 30 "$HOLD"); do
    sleep 30
    p=$(procs); r=$(engine_rss); [ "$p" -gt "$PMAX" ] && PMAX=$p; [ "$r" -gt "$RMAX" ] && RMAX=$r
    if check_all >/dev/null; then echo "  ${t}s: all answer, processes $p, engine RSS ${r} KiB"; else echo "  ${t}s: FAILURES"; holdbad=$((holdbad+1)); fi
done
[ "$holdbad" = 0 ] && ok "everything kept answering for ${HOLD}s" || no "$holdbad checks failed during the hold"
pg=$(curl -s --unix-socket "$SOCK" http://d/thothdock/process-guard | jq -r '.guard.level // empty')
echo "  peak processes $PMAX (baseline $P0), engine RSS peak ${RMAX} KiB (baseline ${R0}), Process Guard level: ${pg:-n/a}"

cleanup; trap - EXIT
sleep 5
left=$(apps_json | jq '.Sandboxes | length')
[ "$left" = 0 ] && ok "no sandbox left after teardown" || no "$left sandboxes left"
P1=$(procs); echo "  processes after teardown $P1 (baseline $P0)"
[ "$P1" -le $((P0 + 2)) ] && ok "processes back to baseline" || no "processes $P1 vs baseline $P0"
# Released addresses are reusable: a fresh sandbox gets one.
ip=$(flatpak run --command=sh org.freedesktop.Platform//24.08 -c 'sleep 3' & sleep 2; apps_json | jq -r '.Sandboxes[0].NetIP'; wait)
[ -n "$ip" ] && [ "$ip" != null ] && ok "a new sandbox gets an address after teardown ($ip)" || no "no address after teardown"
echo "RESULT pass=$pass fail=$fail"
[ "$fail" = 0 ]
