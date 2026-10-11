#!/bin/bash
# Restart policies, events and state consistency on a phone, inside the ThothTerm guest.
# Uses only its own qa_r* containers, one or two at a time (Android phantom-process cap).
#
#   bash restart-device.sh [LOGFILE]        # exit 1 on any FAIL
LOG=${1:-$HOME/restart-device.log}; : > "$LOG"
IMAGE=${TEST_IMAGE:-alpine:3.20}
pass=0; fail=0
ok()  { echo "PASS: $1"; echo "PASS: $1" >> "$LOG"; pass=$((pass+1)); }
bad() { echo "FAIL: $1"; echo "FAIL: $1" >> "$LOG"; fail=$((fail+1)); }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }
st() { docker inspect -f '{{.State.Status}}' "$1" 2>/dev/null; }
rc() { docker inspect -f '{{.RestartCount}}' "$1" 2>/dev/null; }
# Liveness through /proc: the guest shell runs in another SELinux context than the
# app, so kill -0 on app processes is denied even when they are alive.
alive() { [ -d "/proc/$1" ] && ! grep -q '^State:.*Z' "/proc/$1/status" 2>/dev/null; }
waitfor() { # NAME STATUS SECONDS
    for _ in $(seq $(( $3 * 2 ))); do [ "$(st "$1")" = "$2" ] && return 0; sleep 0.5; done; return 1; }
clean() { docker rm -f qa_rno qa_ralways qa_rfail qa_rfail0 qa_runless qa_rloop qa_rlive >/dev/null 2>&1; }
clean
T0=$(date +%s)

docker run -d --name qa_rno "$IMAGE" sh -c 'sleep 1; exit 0' >/dev/null
waitfor qa_rno exited 15; sleep 3
check "restart=no: an exited container stays exited" "$(st qa_rno) $(rc qa_rno)" "exited 0"
docker rm -f qa_rno >/dev/null

docker run -d --name qa_rfail --restart on-failure:2 "$IMAGE" sh -c 'sleep 1; exit 3' >/dev/null
for _ in $(seq 60); do [ "$(rc qa_rfail)" = 2 ] && [ "$(st qa_rfail)" = exited ] && break; sleep 0.5; done; sleep 3
check "on-failure:2 with exit 3: restarted twice, then stays exited" "$(st qa_rfail) $(rc qa_rfail) $(docker inspect -f '{{.State.ExitCode}}' qa_rfail)" "exited 2 3"
docker rm -f qa_rfail >/dev/null
docker run -d --name qa_rfail0 --restart on-failure "$IMAGE" sh -c 'sleep 1; exit 0' >/dev/null
waitfor qa_rfail0 exited 15; sleep 3
check "on-failure with exit 0: not restarted" "$(st qa_rfail0) $(rc qa_rfail0)" "exited 0"
docker rm -f qa_rfail0 >/dev/null

docker run -d --name qa_ralways --restart always "$IMAGE" sh -c 'sleep 2; exit 0' >/dev/null
for _ in $(seq 40); do [ "$(rc qa_ralways)" -ge 2 ] 2>/dev/null && break; sleep 0.5; done
n=$(rc qa_ralways); [ "$n" -ge 2 ] 2>/dev/null && ok "always: restarted after a normal exit ($n restarts)" || bad "always: restarts=$n"
docker stop -t 2 qa_ralways >/dev/null; sleep 4
check "always: an explicit stop is honoured" "$(st qa_ralways)" exited
docker rm -f qa_ralways >/dev/null

docker run -d --name qa_runless --restart unless-stopped "$IMAGE" sleep 100000 >/dev/null
waitfor qa_runless running 15
pid=$(docker inspect -f '{{.State.Pid}}' qa_runless)
alive "$pid" && ok "a Running container has a live process (pid $pid)" || bad "Running but pid $pid is not alive"
docker restart -t 2 qa_runless >/dev/null; waitfor qa_runless running 15
pid2=$(docker inspect -f '{{.State.Pid}}' qa_runless)
[ "$pid2" != "$pid" ] && alive "$pid2" && ok "docker restart replaces the process ($pid -> $pid2)" || bad "restart: $pid -> $pid2"
check "an explicit restart is not counted as a policy restart" "$(rc qa_runless)" 0
docker stop -t 2 qa_runless >/dev/null; sleep 4
check "unless-stopped: an explicit stop stays stopped" "$(st qa_runless)" exited
docker rm -f qa_runless >/dev/null
# A crash (SIGKILL from inside: nothing outside the app may signal its processes).
docker run -d --name qa_rlive --restart unless-stopped "$IMAGE" sh -c 'sleep 2; kill -9 $$' >/dev/null
for _ in $(seq 40); do [ "$(rc qa_rlive)" -ge 1 ] 2>/dev/null && break; sleep 0.5; done
check "unless-stopped: a crashed container (SIGKILL, exit 137) is restarted" "$([ "$(rc qa_rlive)" -ge 1 ] && echo yes) $(docker inspect -f '{{.State.ExitCode}}' qa_rlive)" "yes 137"
docker rm -f qa_rlive >/dev/null

docker run -d --name qa_rloop --restart always "$IMAGE" sh -c 'exit 1' >/dev/null
sleep 8; n1=$(rc qa_rloop); sleep 8; n2=$(rc qa_rloop)
[ "$n1" -ge 3 ] 2>/dev/null && [ "$n2" -lt $(( n1 * 3 )) ] && ok "crash loop backs off ($n1 restarts in 8 s, $n2 in 16 s)" || bad "crash loop: $n1 then $n2"
docker rm -f qa_rloop >/dev/null

# Events: lifecycle order for one container, a pull, a network.
docker events --since "$T0" --until "$(date +%s)" --filter type=container --format '{{.Actor.Attributes.name}} {{.Action}}' > /tmp/qa-events.txt 2>&1
seq_rfail=$(grep '^qa_rfail ' /tmp/qa-events.txt | awk '{print $2}' | tr '\n' ' ')
case "$seq_rfail" in "create start die start die start die destroy "*|"create start die start die start die "*) ok "events: create, start/die x3 for on-failure:2 (${seq_rfail% })" ;; *) bad "events for qa_rfail: '$seq_rfail'" ;; esac
grep -q '^qa_runless kill' /tmp/qa-events.txt || grep -q '^qa_runless die' /tmp/qa-events.txt && ok "events: kill/die recorded" || bad "events: no die for qa_runless"
T1=$(date +%s)
docker pull -q "$IMAGE" >/dev/null; docker network create qa_evnet >/dev/null; docker network rm qa_evnet >/dev/null
sleep 1
ev=$(docker events --since "$T1" --until "$(date +%s)" --format '{{.Type}} {{.Action}}' | sort -u | tr '\n' ';')
case "$ev" in *"image pull"*) ok "events: image pull" ;; *) bad "events: no image pull in '$ev'" ;; esac
case "$ev" in *"network create"*"network destroy"*|*"network destroy"*"network create"*) ok "events: network create and destroy" ;; *) bad "events: networks in '$ev'" ;; esac

# Every container the engine reports running has a live process.
dead=""
for c in $(docker ps -q); do
    p=$(docker inspect -f '{{.State.Pid}}' "$c")
    alive "$p" || dead="$dead $c:$p"
done
[ -z "$dead" ] && ok "every Running container maps to a live process ($(docker ps -q | wc -l) checked)" || bad "running containers without a live process:$dead"
clean
echo "restart-device: $pass passed, $fail failed (log: $LOG)"
[ "$fail" -eq 0 ]
