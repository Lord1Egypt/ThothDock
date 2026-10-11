#!/bin/sh
# Authorization lifecycle of the Web Panel with the real binary: a session
# lives only as long as one panel process.
#
#   tests/panel/lifecycle.sh THOTHDOCK_BINARY
set -u
TD="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
WORK="$(mktemp -d /tmp/tdlife.XXXXXX)"
PORT=17691
BASE="https://127.0.0.1:$PORT"
PID=
fail=0
cleanup() {
    [ -n "$PID" ] && kill -9 "$PID" 2>/dev/null
    case "$WORK" in /tmp/tdlife.*) rm -rf "$WORK" ;; esac
}
trap cleanup EXIT
ok() { echo "PASS: $1"; }
bad() { echo "FAIL: $1"; fail=1; }
expect() { # description want got
    if [ "$2" = "$3" ]; then ok "$1 ($3)"; else bad "$1: want $2, got $3"; fi
}
start() {
    rm -f "$WORK/panel/pairing.json"
    "$TD" panel --root "$WORK" --listen "127.0.0.1:$PORT" > "$WORK/log" 2>&1 &
    PID=$!
    for _ in $(seq 100); do [ -f "$WORK/panel/pairing.json" ] && break; sleep 0.1; done
}
code() { sed -n 's/.*"code": *"\([0-9]*\)".*/\1/p' "$WORK/panel/pairing.json" | head -1; }
status() { curl -sk -o /dev/null -w '%{http_code}' -b "$1" "$BASE/api/session"; }
pair() { # jar
    sleep 1.1
    curl -sk -o /dev/null -w '%{http_code}' -c "$1" -H "Origin: $BASE" -H 'Content-Type: application/json' \
        -d "{\"code\":\"$(code)\"}" "$BASE/pair"
}
fp() { curl -sk -o /dev/null -w '%{ssl_verify_result}' "$BASE/" >/dev/null; openssl x509 -in "$WORK/panel/cert.pem" -noout -fingerprint -sha256 2>/dev/null; }

start
expect "fresh panel needs pairing" 401 "$(status /dev/null)"
expect "pairing browser A" 200 "$(pair "$WORK/a")"
old_code=$(code)
sleep 1.1
expect "paired code cannot be reused" 401 "$(curl -sk -o /dev/null -w '%{http_code}' -H "Origin: $BASE" -H 'Content-Type: application/json' -d "{\"code\":\"$old_code\"}" "$BASE/pair")"
expect "A has a session" 200 "$(status "$WORK/a")"
sessions=$(ls "$WORK/panel" | grep -c sessions || true)
expect "no session file on disk" 0 "$sessions"
fp1=$(fp)

# SIGUSR2: live revoke-all, panel keeps running, a new code is announced.
kill -USR2 "$PID"; sleep 0.5
expect "A after SIGUSR2" 401 "$(status "$WORK/a")"
expect "panel still alive after SIGUSR2" 0 "$(kill -0 "$PID" 2>/dev/null; echo $?)"
expect "re-pairing A with the new code" 200 "$(pair "$WORK/a")"
expect "pairing browser B" 200 "$(kill -USR1 "$PID"; sleep 0.5; pair "$WORK/b")"
expect "B has a session" 200 "$(status "$WORK/b")"
expect "A unaffected by a new code alone" 200 "$(status "$WORK/a")"

# SIGKILL + start = crash recovery.
kill -9 "$PID"; wait "$PID" 2>/dev/null; PID=
expect "listener closed after kill" 000 "$(status "$WORK/a")"
start
expect "A after crash recovery" 401 "$(status "$WORK/a")"
expect "B after crash recovery" 401 "$(status "$WORK/b")"
[ "$(fp)" = "$fp1" ] && ok "certificate unchanged across restarts" || bad "certificate changed"

# Legacy session file from an older version.
expect "pairing C" 200 "$(pair "$WORK/c")"
# SIGTERM + start = Stop -> Start.
kill "$PID"; wait "$PID" 2>/dev/null; PID=
[ -e "$WORK/panel/pairing.json" ] && bad "pairing.json left after stop" || ok "pairing code removed on stop"
printf '{"sessions":{}}' > "$WORK/panel/sessions.json"
start
[ -e "$WORK/panel/sessions.json" ] && bad "legacy sessions.json survived start" || ok "legacy sessions.json discarded at start"
expect "C after Stop->Start" 401 "$(status "$WORK/c")"
expect "fresh pairing after restart" 200 "$(pair "$WORK/d")"
expect "dashboard session after restart" 200 "$(status "$WORK/d")"
exit $fail
