#!/bin/sh
# User-defined networks end to end (tickets P2-01..P2-05): the stock Docker
# CLI against ThothDock and a Garden PRoot with patch 0009 (--net-ip).
#
#   tests/regression/networks.sh THOTHDOCK_BINARY GARDEN_PROOT_BINARY
#
# Needs ss and python3 on the host. Two web servers listen on port 80 at the
# same time; names, aliases, the private localhost, -p, the gateway, connect
# and disconnect, and a SIGKILLed daemon are checked.
set -eu
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
PROOT="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
IMAGE="${TEST_IMAGE:-alpine:3.20}"
command -v docker >/dev/null || { echo "SKIP: no docker CLI"; exit 2; }
"$PROOT" --help | grep -q -- --net-ip || { echo "SKIP: $PROOT has no --net-ip (Garden patch 0009)"; exit 2; }
WORK="$(mktemp -d /tmp/tdnet.XXXXXX)"
export DOCKER_HOST="unix://$WORK/run/thothdock.sock"
export DOCKER_CONFIG="$WORK/cli"
unset DOCKER_CONTEXT
PID= HOSTSRV=
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
    [ -n "$HOSTSRV" ] && kill "$HOSTSRV" 2>/dev/null
    case "$WORK" in /tmp/tdnet.*) rm -rf "$WORK" "$WORK.log" ;; esac
}
trap cleanup EXIT
# A tiny HTTP server: answers every connection on PORT with BODY.
web() { # NAME BODY PORT [docker run options...]
    name=$1 body=$2 port=$3; shift 3
    docker run -d --name "$name" "$@" "$IMAGE" sh -c \
        "while true; do printf 'HTTP/1.0 200 OK\r\n\r\n$body\n' | nc -l -p $port; done" >/dev/null
}
get() { # CONTAINER URL: what wget prints inside CONTAINER
    docker exec "$1" wget -q -T 5 -O- "$2" 2>/dev/null || true
}
retry_get() { # same, retried while servers start
    for _ in $(seq 30); do out=$(get "$1" "$2"); [ -n "$out" ] && { echo "$out"; return; }; sleep 0.2; done
}

start_daemon
docker pull -q "$IMAGE" >/dev/null
ss -Hltn | awk '{print $4}' | sort > "$WORK.before"

docker network create demo >/dev/null
check "docker network ls" "$(docker network ls --format '{{.Name}}' | sort | tr '\n' ' ')" "bridge demo host none "
check "network subnet" "$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{.Gateway}}{{end}}' demo)" "127.77.0.0/16 127.77.0.1"

web web from-web 80 --network demo
web web2 from-web2 80 --network demo --network-alias api
docker run -d --name cli --network demo "$IMAGE" sleep 100000 >/dev/null
check "two containers serve port 80 at once; reached by name" "$(retry_get cli http://web/)" from-web
check "the second one, by name" "$(retry_get cli http://web2/)" from-web2
check "by --network-alias" "$(retry_get cli http://api/)" from-web2
check "localhost inside a container is its own" "$(retry_get web http://localhost/)" from-web
ip=$(docker inspect -f '{{.NetworkSettings.Networks.demo.IPAddress}}' web)
case "$ip" in 127.77.*) pass "inspect reports the container address ($ip)" ;; *) fail "address '$ip'" ;; esac
check "reached by address" "$(retry_get cli "http://$ip/")" from-web

# -p to an addressed container: host port 80 inside, 18080 outside.
web pubweb from-pubweb 80 --network demo -p 127.0.0.1:18080:80
out=
for _ in $(seq 30); do out=$(python3 -I -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:18080/", timeout=3).read().decode().strip())' 2>/dev/null || true); [ -n "$out" ] && break; sleep 0.2; done
check "-p forwards to the container's own address" "$out" from-pubweb

# Nothing a workload listens on became reachable beyond loopback.
ss -Hltn | awk '{print $4}' | sort > "$WORK.after"
new=$(comm -13 "$WORK.before" "$WORK.after")
echo "$new" | grep -Eq '^(0\.0\.0\.0|\*|\[::\]):' && fail "a new wildcard listener: $(echo "$new" | tr '\n' ' ')" || pass "no new wildcard listener ($(echo "$new" | tr '\n' ' '))"

# host.docker.internal is the device's loopback.
python3 -I -c '
import http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"from-host\n")
    def log_message(self, *a): pass
socketserver.TCPServer(("127.0.0.1", 24399), H).serve_forever()' &
HOSTSRV=$!
check "host.docker.internal reaches the device's 127.0.0.1" "$(retry_get cli http://host.docker.internal:24399/)" from-host

# Membership decides who resolves whom.
docker network create other >/dev/null
docker run -d --name iso --network other "$IMAGE" sleep 100000 >/dev/null
check "a container on another network does not resolve web" "$(docker exec iso sh -c 'getent hosts web || echo none')" none
docker network connect demo iso
check "after network connect it does" "$(retry_get iso http://web/)" from-web
docker network disconnect demo iso
check "after network disconnect it no longer does" "$(docker exec iso sh -c 'getent hosts web || echo none')" none

docker network rm demo >/dev/null 2>&1 && fail "removed a network in use" || pass "a network in use cannot be removed"

# A device-network container keeps v0.1.1 behaviour.
check "device-network containers are unchanged" "$(docker run --rm "$IMAGE" sh -c 'getent hosts web || echo none')" none

# SIGKILL the daemon; restore the stack; addresses and names survive.
for c in web web2 cli; do docker update --restart unless-stopped "$c" >/dev/null; done
kill -9 "$PID"; wait "$PID" 2>/dev/null || true; PID=
sleep 1
check "no container process outlives a killed daemon" "$(orphans)" 0
start_daemon
check "after a daemon restart the stack resolves and serves again" "$(retry_get cli http://web2/)" from-web2
check "and the address is the same" "$(docker inspect -f '{{.NetworkSettings.Networks.demo.IPAddress}}' web)" "$ip"

docker rm -f web web2 cli pubweb iso >/dev/null
docker network rm demo other >/dev/null && pass "networks removed once empty" || fail "network rm"
check "only built-in networks remain" "$(docker network ls --format '{{.Name}}' | sort | tr '\n' ' ')" "bridge host none "
check "no container process left" "$(orphans)" 0
echo "networks: $fails failure(s)"
[ "$fails" -eq 0 ]
