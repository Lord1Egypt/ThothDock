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
# Outbound-connection probe (needs gcc and a default route; skipped otherwise). 192.0.2.1 is TEST-NET-1:
# routable through the default route, never answered, and not a local address (a connect to one of
# the machine's own addresses succeeds even from a loopback source, which would hide the defect).
PROBE_IP=192.0.2.1
mkdir -p "$WORK.probe"
BINDS=
if command -v gcc >/dev/null && gcc -static -O1 -o "$WORK.probe/netprobe" "$(dirname "$0")/netprobe.c" 2>/dev/null; then
    BINDS="--allow-bind $WORK.probe"
fi
start_daemon() {
    "$TD" serve --root "$WORK" --proot "$PROOT" $BINDS >> "$WORK.log" 2>&1 &
    PID=$!
    for _ in $(seq 100); do docker version >/dev/null 2>&1 && return 0; sleep 0.1; done
    echo "daemon did not start"; cat "$WORK.log"; exit 1
}
orphans() { ps -eo args | grep -c "^$PROOT --rootfs=$WORK/" || true; }
cleanup() {
    [ -n "$PID" ] && { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; }
    [ -n "$HOSTSRV" ] && kill "$HOSTSRV" 2>/dev/null
    case "$WORK" in /tmp/tdnet.*) rm -rf "$WORK" "$WORK.log" "$WORK.before" "$WORK.after" "$WORK.after2" "$WORK.probe" ;; esac
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

# Outbound connections. A client that binds the wildcard before it connects (musl's resolver) must
# still reach an address outside loopback; PRoot --net-ip once broke it with EINVAL, which made
# every DNS lookup from a container on a user network fail.
if [ -x "$WORK.probe/netprobe" ] && docker run --rm -v "$WORK.probe:/mnt" "$IMAGE" sh -c "/mnt/netprobe $PROBE_IP 9" 2>&1 | grep -q "udp, connect only: ok"; then
    out=$(docker run --rm --network demo -v "$WORK.probe:/mnt" "$IMAGE" sh -c "/mnt/netprobe $PROBE_IP 9" 2>&1 || true)
    echo "$out" | grep -q "Invalid argument" && fail "outbound from a user network: $(echo "$out" | grep "Invalid argument" | tr '\n' ';')" || pass "bind-then-connect to an outside address works on a user network (udp, tcp, dual-stack)"
    control=$(docker run --rm -v "$WORK.probe:/mnt" "$IMAGE" sh -c "/mnt/netprobe $PROBE_IP 9" 2>&1 || true)
    echo "$control" | grep -q "Invalid argument" && fail "the probe itself fails on the device network" || pass "the same probe on the device network (control)"
else
    echo "SKIP: outbound probe (needs gcc and a default route)"
fi

# What the built-in networks are, as the stock CLI sees it (labels carry the truth).
check "bridge is reported as the device network" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.kind"}}' bridge)" device-bridge
check "host is reported as the device network" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.kind"}}' host)" device-host
check "none is reported as unsupported" "$(docker network inspect -f '{{index .Labels "io.thothdock.network.supported"}}' none)" false
check "built-in networks claim no address range" "$(docker network inspect -f '{{len .IPAM.Config}}' bridge host none | tr '\n' ' ')" "0 0 0 "
check "the user network claims its range" "$(docker network inspect -f '{{len .IPAM.Config}}' demo)" 1

# Unsupported modes fail loudly, never fall back to the device network, and leave nothing behind.
before=$(docker ps -aq | wc -l)
if out=$(docker run --rm --network none "$IMAGE" true 2>&1); then fail "--network none ran"; else
    echo "$out" | grep -q -- "--network none" && pass "--network none is refused explicitly" || fail "--network none refusal text: $out"
fi
check "the refused run left no container" "$(docker ps -aq | wc -l)" "$before"
if docker run -d --network nosuchnet "$IMAGE" true >/dev/null 2>&1; then fail "an unknown network was accepted"; else pass "an unknown network is refused"; fi
if docker run -d --network container:web "$IMAGE" true >/dev/null 2>&1; then fail "container: network mode was accepted"; else pass "--network container: is refused"; fi

# Invalid membership is rejected, with an honest reason.
refused() { # NAME PATTERN CMD...
    n=$1 pat=$2; shift 2
    if out=$("$@" 2>&1); then fail "$n (succeeded)"; else echo "$out" | grep -qi -- "$pat" && pass "$n" || fail "$n: $out"; fi
}
refused "connect to an unknown network" "not found" docker network connect nosuchnet iso
refused "connect to none" "not implemented" docker network connect none iso
refused "connect to bridge" "Android device network" docker network connect bridge iso
refused "connect to host" "Android device network" docker network connect host iso
refused "connect twice" "already exists in network" docker network connect other iso
refused "disconnect from a network never joined" "is not connected" docker network disconnect demo iso
refused "connect an unknown container" "No such container" docker network connect demo ghostcontainer
refused "create a network named like a built-in" "pre-defined" docker network create bridge
refused "remove a built-in network" "pre-defined" docker network rm none

# An alias given at connect resolves for the other members, and stops when the container leaves.
docker network connect --alias backend demo iso
check "an alias given at connect resolves on that network" "$(docker exec cli sh -c 'getent hosts backend | cut -d" " -f1 | head -c3')" 127
# An alias on another network does not shadow a name on this one.
docker run -d --name iso2 --network other --network-alias web "$IMAGE" sleep 100000 >/dev/null
check "an alias on another network does not shadow web" "$(docker exec cli getent hosts web | awk '{print $1}')" "$ip"
# KNOWN LIMITATION (docs/nextgen/SECURITY_MODEL.md): addresses are separation, not isolation.
check "KNOWN LIMITATION: a container that knows web's address reaches it from another network" "$(docker network disconnect demo iso && retry_get iso "http://$ip/")" from-web
check "an alias stops resolving after disconnect" "$(docker exec cli sh -c 'getent hosts backend || echo none')" none
docker rm -f iso2 >/dev/null
ss -Hltn | awk '{print $4}' | sort > "$WORK.after2"
new=$(comm -13 "$WORK.before" "$WORK.after2")
echo "$new" | grep -Eq '^(0\.0\.0\.0|\*|\[::\]):' && fail "a new wildcard listener after the network operations: $(echo "$new" | tr '\n' ' ')" || pass "no wildcard listener after connect/disconnect/alias operations"

# network prune removes only unused user-defined networks.
docker network create spare >/dev/null
out=$(docker network prune -f 2>&1)
echo "$out" | grep -qx spare && pass "prune removed the unused user network" || fail "prune output: $out"
check "prune kept networks in use and the built-in ones" "$(docker network ls --format '{{.Name}}' | sort | tr '\n' ' ')" "bridge demo host none other "
case "$out" in *demo*|*other*|*bridge*|*host*|*none*) fail "prune touched something in use or built in: $out" ;; *) pass "prune named only the unused network" ;; esac

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
