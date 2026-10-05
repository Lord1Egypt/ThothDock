#!/bin/bash
# Hostile API checks against the live daemon, from inside the ThothTerm
# terminal (needs curl; installs it with sudo apt-get when missing).
#
#   bash hostile.sh [LOGFILE]       # exit 1 on any FAIL
LOG=${1:-$HOME/hostile.log}; : > $LOG
pass=0; fail=0
SOCK=${DOCKER_HOST#unix://}; SOCK=${SOCK:-/run/thothdock/thothdock.sock}
command -v curl >/dev/null || sudo -n apt-get install -y curl >/dev/null 2>&1
api() { curl -s -o /tmp/h.out -w '%{http_code}' --unix-socket "$SOCK" "$@"; }
chk() { # chk name want_code cmd...
  name=$1; want=$2; shift 2
  got=$("$@"); body=$(head -c 160 /tmp/h.out | tr '\n' ' ')
  echo "$name -> $got $body" | tee -a $LOG
  if [[ "$got" =~ ^($want)$ ]]; then echo "PASS: $name" | tee -a $LOG; pass=$((pass+1)); else echo "FAIL: $name (want $want)" | tee -a $LOG; fail=$((fail+1)); fi
}
H='Content-Type: application/json'
chk "malformed JSON on create" 400 api -X POST -H "$H" -d '{not json' "http://d/v1.41/containers/create"
chk "truncated JSON on create" 400 api -X POST -H "$H" -d '{"Image":"alpine","Cmd":[' "http://d/v1.41/containers/create"
chk "empty body on create" 400 api -X POST -H "$H" "http://d/v1.41/containers/create"
head -c 2000000 /dev/zero | tr '\0' 'a' > /tmp/big.json
chk "2 MB body refused" 400 api -X POST -H "$H" --data-binary @/tmp/big.json "http://d/v1.41/containers/create"
chk "traversal volume name" 400 api -X POST -H "$H" -d '{"Name":"../escape"}' "http://d/v1.41/volumes/create"
chk "absolute volume name" 400 api -X POST -H "$H" -d '{"Name":"/etc"}' "http://d/v1.41/volumes/create"
chk "NUL in volume name" 400 api -X POST -H "$H" -d '{"Name":"a\u0000b"}' "http://d/v1.41/volumes/create"
chk "oversized volume name" 400 api -X POST -H "$H" -d "{\"Name\":\"$(head -c 300 /dev/zero | tr '\0' a)\"}" "http://d/v1.41/volumes/create"
chk "bind of / refused" 403 api -X POST -H "$H" -d '{"Image":"alpine","HostConfig":{"Binds":["/:/host"]}}' "http://d/v1.41/containers/create"
# A path the host does not have, or the data root: either way refused, never accepted.
chk "bind of a path outside the allowed roots refused" "400|403" api -X POST -H "$H" -d "{\"Image\":\"alpine\",\"HostConfig\":{\"Binds\":[\"$HOME/..:/host\"]}}" "http://d/v1.41/containers/create"
chk "privileged refused" 501 api -X POST -H "$H" -d '{"Image":"alpine","HostConfig":{"Privileged":true}}' "http://d/v1.41/containers/create"
chk "unknown API version too new" 400 api "http://d/v9.99/containers/json"
# Go's router redirects a non-canonical path to its clean form; the target does not exist.
chk "path traversal in container id is not served" "307|404" api "http://d/v1.41/containers/..%2f..%2fetc/json"
chk "unknown container logs" 404 api "http://d/v1.41/containers/nonexistent/logs?stdout=1"
chk "exec on a missing container" 404 api -X POST -H "$H" -d '{"Cmd":["id"]}' "http://d/v1.41/containers/nonexistent/exec"
chk "archive endpoint is not implemented" 501 api "http://d/v1.41/containers/nonexistent/archive?path=/etc/passwd"
chk "UDP publish refused" 501 api -X POST -H "$H" -d '{"Image":"alpine","HostConfig":{"PortBindings":{"53/udp":[{"HostPort":"5353"}]}}}' "http://d/v1.41/containers/create"
chk "the redirect target is not found" 404 api "http://d/etc/json"
# The daemon is still healthy after all of that.
chk "ping after abuse" 200 api "http://d/_ping"
docker run --rm alpine echo still-serving | tee -a $LOG | grep -q still-serving && { echo "PASS: runs containers after abuse" | tee -a $LOG; pass=$((pass+1)); } || { echo "FAIL: runs containers after abuse" | tee -a $LOG; fail=$((fail+1)); }
rm -f /tmp/big.json /tmp/h.out
echo "RESULT pass=$pass fail=$fail" | tee -a $LOG
[ $fail = 0 ]
