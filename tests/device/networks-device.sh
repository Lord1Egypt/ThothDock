#!/bin/bash
# Network semantics on a phone, inside the ThothTerm guest (or as guest root through a
# harness). Creates and removes only its own qa_* networks and containers; existing
# workloads are left alone. Needs the network for the outbound checks.
#
# Android kills an app's child processes beyond a device-wide cap (about 32 "phantom
# processes"), and the engine with them. Each container here is 2-3 processes, so test
# containers are removed as soon as their checks are done; check the headroom first
# (docs/nextgen/ANDROID_LIFECYCLE.md).
#
#   bash networks-device.sh [LOGFILE]        # exit 1 on any FAIL
LOG=${1:-$HOME/networks-device.log}; : > "$LOG"
IMAGE=${TEST_IMAGE:-alpine:3.20}
pass=0; fail=0
ok()  { echo "PASS: $1"; echo "PASS: $1" >> "$LOG"; pass=$((pass+1)); }
bad() { echo "FAIL: $1"; echo "FAIL: $1" >> "$LOG"; fail=$((fail+1)); }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }
refused() { n=$1 pat=$2; shift 2; if out=$("$@" 2>&1); then bad "$n (succeeded)"; else echo "$out" | grep -qi -- "$pat" && ok "$n" || bad "$n: $out"; fi; }
web() { docker run -d --name "$1" "${@:3}" "$IMAGE" sh -c "while true; do printf 'HTTP/1.0 200 OK\r\n\r\n$2\n' | nc -l -p 80; done" >/dev/null; }
get() { for _ in $(seq 30); do o=$(docker exec "$1" wget -q -T 5 -O- "$2" 2>/dev/null); [ -n "$o" ] && { echo "$o"; return; }; sleep 0.3; done; }
cleanup() { docker rm -f qa_web qa_web2 qa_cli qa_iso qa_pub >/dev/null 2>&1; docker network rm qa_n1 qa_n2 qa_spare >/dev/null 2>&1; }
cleanup
BEFORE=$(docker ps -aq | sort | tr '\n' ' ')
UNLABELLED=$(docker network ls --format '{{.Name}}' | grep -vcE '^(bridge|host|none)$')
docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull -q "$IMAGE" >/dev/null

# Built-in networks say what they are; none is not usable.
check "bridge reported as the device network" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.kind"}}' bridge)" device-bridge
check "host reported as the device network" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.kind"}}' host)" device-host
check "none reported as unsupported" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.supported"}}' none)" false
refused "--network none refused" "--network none" docker run --rm --network none "$IMAGE" true

docker network create --label qa.networks=1 qa_n1 >/dev/null && docker network create --label qa.networks=1 qa_n2 >/dev/null
check "user network range" "$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' qa_n1)" 127.77.0.0/16
web qa_web from-web --network qa_n1
web qa_web2 from-web2 --network qa_n1 --network-alias qa_api
web qa_pub from-pub --network qa_n1 -p 127.0.0.1:18081:80
docker run -d --name qa_cli --network qa_n1 "$IMAGE" sleep 100000 >/dev/null
check "two services on port 80, reached by name" "$(get qa_cli http://qa_web/)" from-web
check "the second one by name" "$(get qa_cli http://qa_web2/)" from-web2
check "network alias" "$(get qa_cli http://qa_api/)" from-web2
check "localhost is the container's own" "$(get qa_web http://localhost/)" from-web
ip=$(docker inspect -f '{{.NetworkSettings.Networks.qa_n1.IPAddress}}' qa_web)
case "$ip" in 127.77.*) ok "container address $ip" ;; *) bad "container address '$ip'" ;; esac
pub=""; for _ in $(seq 30); do pub=$(wget -q -T 3 -O- http://127.0.0.1:18081/ 2>/dev/null || curl -s -m 3 http://127.0.0.1:18081/ 2>/dev/null); [ -n "$pub" ] && break; sleep 0.3; done
check "published port reaches the right container" "$pub" from-pub
docker rm -f qa_pub qa_web2 >/dev/null

# Membership: names follow it; addresses do not (documented limitation).
docker run -d --name qa_iso --network qa_n2 "$IMAGE" sleep 100000 >/dev/null
check "another network does not resolve qa_web" "$(docker exec qa_iso sh -c 'getent hosts qa_web || echo none')" none
check "KNOWN LIMITATION: qa_web reachable by address from another network" "$(get qa_iso "http://$ip/")" from-web
refused "connect to an unknown network" "not found" docker network connect qa_nosuch qa_iso
refused "connect to none" "not implemented" docker network connect none qa_iso
refused "connect to bridge" "Android device network" docker network connect bridge qa_iso
refused "connect twice" "already exists" docker network connect qa_n2 qa_iso
docker network connect --alias qa_backend qa_n1 qa_iso
check "alias given at connect resolves" "$(docker exec qa_cli sh -c 'getent hosts qa_backend >/dev/null && echo yes || echo no')" yes
docker network disconnect qa_n1 qa_iso
check "alias stops resolving after disconnect" "$(docker exec qa_cli sh -c 'getent hosts qa_backend >/dev/null && echo yes || echo no')" no
check "device-network containers do not see qa names" "$(docker run --rm "$IMAGE" sh -c 'getent hosts qa_web || echo none')" none
docker rm -f qa_iso qa_cli >/dev/null

# Outbound from a user network (once broken: PRoot moved bind(0.0.0.0:0) to 127.77.x.y).
check "DNS from a user network" "$(docker run --rm --network qa_n1 "$IMAGE" sh -c 'nslookup ton.org >/dev/null 2>&1 && echo ok || echo fail')" ok
check "HTTPS from a user network" "$(docker run --rm --network qa_n1 "$IMAGE" sh -c 'wget -q -T 15 -O- https://ton.org/global-config.json | grep -q config.global && echo ok')" ok

# Prune removes only unused user networks (filtered to this script's label, so the owner's
# unused networks are never touched).
docker network create --label qa.networks=1 qa_spare >/dev/null
out=$(docker network prune -f --filter label=qa.networks=1 2>&1)
echo "$out" | grep -qx qa_spare && ok "prune removed the unused network" || bad "prune: $out"
# qa_n1 is in use (qa_web); the built-in networks and everything without the label must remain.
left=$(docker network ls --format '{{.Name}}' | sort | tr '\n' ' ')
missing=""; for n in bridge host none qa_n1; do echo " $left " | grep -q " $n " || missing="$missing $n"; done
[ -z "$missing" ] && ok "prune spared the network in use and the built-in ones" || bad "prune removed:$missing"
check "networks without the label untouched by prune" "$(echo "$left" | tr ' ' '\n' | grep -vcE '^(qa_n1|qa_n2|bridge|host|none|)$')" "$UNLABELLED"

cleanup
check "only this script's resources were removed" "$(docker ps -aq | sort | tr '\n' ' ')" "$BEFORE"
echo "networks-device: $pass passed, $fail failed (log: $LOG)"
[ "$fail" -eq 0 ]
