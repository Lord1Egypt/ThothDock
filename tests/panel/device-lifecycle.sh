#!/bin/bash
# The owner's reported sequence on a real phone, with the panel started from the
# Android UI: pair -> authenticated -> Stop -> Start -> reopen with the old cookie.
# Also Disconnect all devices, and an old session's try at container actions.
#
#   ADB="adb -s SERIAL" PKG=com.thothterm.debian.qa.nextgen \
#     tests/panel/device-lifecycle.sh https://PHONE:7690 DISPOSABLE_CONTAINER
#
# The Web Panel must be running (Wi-Fi mode) and the app in front. Pairing codes,
# cookies and CSRF tokens are used but never printed. DISPOSABLE_CONTAINER must be
# a throwaway container: it is started and stopped. Exit 0 only if every check passes.
set -u
URL=$1 CT=$2
ADB=${ADB:-adb}; PKG=${PKG:-com.thothterm.debian.qa.nextgen}; export ADB PKG
UIA="$(dirname "$0")/../device/uia.sh"
W=$(mktemp -d); trap 'rm -rf $W' EXIT
fail=0
expect() { if [ "$2" = "$3" ]; then echo "PASS: $1 ($3)"; else echo "FAIL: $1: want $2 got $3"; fail=1; fi; }
code() { $ADB shell "run-as $PKG cat files/thothdock/panel/pairing.json" | tr -d '\r' | sed -n 's/.*"code": *"\([0-9]*\)".*/\1/p'; }
fp() { $ADB shell "run-as $PKG cat files/thothdock/panel/pairing.json" | tr -d '\r' | sed -n 's/.*"fingerprint": *"\([^"]*\)".*/\1/p'; }
pair() { # jar -> http status; csrf into $1.csrf
    sleep 1.2
    curl -sk -m 15 -o "$1.body" -w '%{http_code}' -c "$1" -H "Origin: $URL" -H 'Content-Type: application/json' -d "{\"code\":\"$(code)\"}" "$URL/pair"
    sed -n 's/.*"csrf":"\([^"]*\)".*/\1/p' "$1.body" > "$1.csrf"
}
get() { curl -sk -m 15 -o /dev/null -w '%{http_code}' -b "$1" "$URL$2"; }
act() { curl -sk -m 30 -o /dev/null -w '%{http_code}' -b "$1" -X "$3" -H "Origin: $URL" -H "X-ThothDock-CSRF: $(cat "$1.csrf")" "$URL$2"; }
# tap scrolls the Web Panel page (up, then back down) until the button is on screen.
tap() {
    "$UIA" top >/dev/null 2>&1; sleep 1
    for i in 1 2 3 4 5 6; do
        "$UIA" tap "$1" >/dev/null 2>&1 && { sleep 4; return; }
        "$UIA" scroll >/dev/null 2>&1; sleep 1
    done
    echo "FAIL: UI tap '$1' (not found)"; fail=1
}

FP1=$(fp)
expect "no cookie is refused" 401 "$(get /dev/null /api/session)"
tap "New code"
expect "pair browser A" 200 "$(pair $W/a)"
expect "A authorised" 200 "$(get $W/a /api/session)"
expect "A refresh / navigation" 200 "$(get $W/a /api/containers)"
tap "Stop Web Panel"
expect "panel listener closed after Stop" 000 "$(get $W/a /api/session)"
tap "Start Web Panel"
# Stop -> Start happened just above: every old credential must be dead.
expect "A after Stop->Start" 401 "$(get $W/a /api/session)"
expect "A may not start a container" 401 "$(act $W/a /api/containers/$CT/start POST)"
expect "A may not stop a container" 401 "$(act $W/a /api/containers/$CT/stop POST)"
expect "A may not restart a container" 401 "$(act $W/a /api/containers/$CT/restart POST)"
expect "A may not delete a container" 401 "$(act $W/a /api/containers/$CT DELETE)"
expect "A event stream refused" 401 "$(get $W/a /api/events)"
expect "fingerprint unchanged by Stop/Start" "$FP1" "$(fp)"
expect "pair again after restart (A)" 200 "$(pair $W/a2)"
expect "fresh session works" 200 "$(get $W/a2 /api/session)"
expect "fresh session may stop the disposable container" 200 "$(act $W/a2 /api/containers/$CT/stop POST)"
expect "fresh session may start it" 200 "$(act $W/a2 /api/containers/$CT/start POST)"

# Two browsers, then Disconnect all devices: both die at once, panel keeps listening.
tap "New code"
expect "pair browser B" 200 "$(pair $W/b)"
expect "A2 and B both authorised" "200 200" "$(get $W/a2 /api/session) $(get $W/b /api/session)"
curl -sk -m 3600 -N -b $W/b "$URL/api/events" -o $W/stream >/dev/null 2>&1 & SP=$!
sleep 2
tap "Disconnect all devices" >/dev/null
expect "A2 after Disconnect all" 401 "$(get $W/a2 /api/session)"
expect "B after Disconnect all" 401 "$(get $W/b /api/session)"
sleep 1
if kill -0 $SP 2>/dev/null; then echo "FAIL: B's event stream outlived its session"; kill $SP; fail=1; else echo "PASS: B's event stream ended"; fi
expect "panel still listening, new code shown" 200 "$(pair $W/c)"
expect "fresh session after Disconnect all" 200 "$(get $W/c /api/session)"
expect "fingerprint unchanged by Disconnect all" "$FP1" "$(fp)"
exit $fail
