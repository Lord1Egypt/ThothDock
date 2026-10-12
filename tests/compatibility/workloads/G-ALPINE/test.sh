# G-ALPINE: minimal Linux semantics on Alpine.
# timeout: 240
. ../G-ALPINE/minimal.sh
minimal_env alpine:3.22
https=$(dk run --rm --network "$N" alpine:3.22 wget -q -O - https://dns.google/resolve?name=example.com 2>&1 | grep -c Answer)
ok "$([ "$https" -gt 0 ]; echo $?)" "HTTPS request with busybox wget (TLS)"
no_leftovers
