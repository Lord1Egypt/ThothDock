# C-COREDNS: CoreDNS (Go) answering from an explicit Corefile over UDP and
# TCP on a user network, with a timeout and NXDOMAIN case. Publishing UDP on
# a host port is unsupported by design (documented), so clients are other
# containers.
# timeout: 240
IMG=coredns/coredns:1.12.1; CLI=alpine:3.22
dk pull -q $IMG >/dev/null; dk pull -q $CLI >/dev/null
setup
V=$(name conf); dk volume create "$V" >/dev/null
dk run --rm -v "$V":/c $CLI sh -c 'printf ".:5300 {\n  hosts {\n    10.9.8.7 app.test\n    fallthrough\n  }\n  template IN A example.invalid {\n    rcode NXDOMAIN\n  }\n  log\n  errors\n}\n" > /c/Corefile'
run_bg dns -v "$V":/c $IMG -conf /c/Corefile
D=$(name dns)
q() { dk run --rm --network "$N" $CLI sh -c "apk add --no-cache bind-tools >/dev/null 2>&1; dig +time=3 +tries=1 $* -p 5300 @$D"; }
wait_for 60 sh -c "dk run --rm --network $N $CLI nslookup -port=5300 app.test $D 2>/dev/null | grep -q 10.9.8.7"
ok $? "UDP query (nslookup) answered"
ok "$([ "$(q +short app.test A)" = 10.9.8.7 ]; echo $?)" "dig over UDP: A record from the hosts plugin"
ok "$([ "$(q +tcp +short app.test A)" = 10.9.8.7 ]; echo $?)" "dig over TCP"
ok "$(q example.invalid A | grep -q 'status: NXDOMAIN'; echo $?)" "NXDOMAIN from the template plugin"
ok "$(dk run --rm --network "$N" $CLI sh -c "apk add --no-cache bind-tools >/dev/null 2>&1; dig +time=2 +tries=1 +short app.test A -p 5399 @$D; echo rc=\$?" | grep -q 'connection refused\|timed out\|no servers\|rc=[19]'; echo $?)" "a closed port gives a clean failure, not a hang"
n=$(dk run --rm --network "$N" $CLI sh -c "apk add --no-cache bind-tools >/dev/null 2>&1; i=0; while [ \$i -lt 40 ]; do dig +time=3 +tries=1 +short app.test A -p 5300 @$D & i=\$((i+1)); done; wait" | grep -c '^10.9.8.7$')
ok "$([ "$n" = 40 ]; echo $?)" "40 concurrent UDP queries ($n answered)"
dk stop -t 10 "$D" >/dev/null
ok "$(c=$(exit_code "$D"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "graceful stop (exit $(exit_code "$D"))"
unsupported "publishing a UDP port on the host (-p ...:53/udp) is refused by design"
ok "$(dk run --rm -p 15353:53/udp $CLI true 2>&1 | grep -qi 'udp'; echo $?)" "-p .../udp is refused with an explanation"
logs_on_fail dns
no_leftovers
