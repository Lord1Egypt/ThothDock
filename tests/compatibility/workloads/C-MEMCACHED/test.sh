# C-MEMCACHED: memcached 1.6 (threads, libevent), text protocol over nc from
# another container.
# timeout: 180
IMG=memcached:1.6-alpine
dk pull -q $IMG >/dev/null; dk pull -q alpine:3.22 >/dev/null
setup
run_bg mc $IMG memcached -m 32 -t 4
H=$(name mc)
mc() { printf "$1" | dk run -i --rm --network "$N" alpine:3.22 nc -w 2 "$H" 11211 | tr -d '\r'; }
wait_for 30 sh -c "printf 'version\r\nquit\r\n' | dk run -i --rm --network $N alpine:3.22 nc -w 2 $H 11211 | grep -q VERSION"
ok $? "server answers from another container"
ok "$(mc 'set k 0 60 5\r\nhello\r\nget k\r\nquit\r\n' | grep -q '^hello$'; echo $?)" "set/get"
mc 'set t 0 1 1\r\nx\r\nquit\r\n' >/dev/null; sleep 2
ok "$(mc 'get t\r\nquit\r\n' | grep -q '^VALUE'; [ $? != 0 ]; echo $?)" "1 s expiry"
i=0; while [ $i -lt 10 ]; do mc "set c$i 0 60 1\r\n$i\r\nquit\r\n" >/dev/null & i=$((i + 1)); done; wait
ok "$([ "$(mc 'get c0 c1 c2 c3 c4 c5 c6 c7 c8 c9\r\nquit\r\n' | grep -c '^VALUE')" = 10 ]; echo $?)" "10 concurrent clients"
note "$(mc 'stats\r\nquit\r\n' | grep -E 'STAT (threads|curr_connections|bytes) ' | tr '\n' ' ')"
dk stop -t 10 "$H" >/dev/null
ok "$(c=$(exit_code "$H"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "docker stop ends memcached (exit $(exit_code "$H"))"
dk start "$H" >/dev/null
wait_for 30 sh -c "printf 'version\r\nquit\r\n' | dk run -i --rm --network $N alpine:3.22 nc -w 2 $H 11211 | grep -q VERSION"
ok $? "restart serves again (cache is volatile by design)"
logs_on_fail mc
no_leftovers
