# kv_store IMAGE SERVER CLI BENCH: Redis-protocol store (Redis, Valkey).
kv_store() {
    IMG=$1 SRV=$2 CLI=$3 BENCH=$4
    dk pull -q "$IMG" >/dev/null || { ok 1 "pull $IMG"; return; }
    setup
    V=$(name data); dk volume create "$V" >/dev/null
    run_bg kv -v "$V":/data "$IMG" $SRV --appendonly yes --appendfsync always
    H=$(name kv)
    cli() { dk run --rm --network "$N" "$IMG" $CLI -h "$H" "$@"; }
    wait_for 60 sh -c "[ \"\$(dk run --rm --network $N $IMG $CLI -h $H ping)\" = PONG ]"
    ok $? "server answers PING from another container"
    ok "$([ "$(cli set greeting hello)" = OK ] && [ "$(cli get greeting)" = hello ]; echo $?)" "SET/GET"
    cli set temp x EX 1 >/dev/null; sleep 3
    ok "$([ -z "$(cli get temp)" ]; echo $?)" "key with a 1 s TTL expires"
    ok "$([ "$(cli incrby counter 5)" = 5 ] && [ "$(cli lpush l a b c)" = 3 ] && [ "$(cli lrange l 0 0)" = c ]; echo $?)" "counters and lists"
    b=$(dk run --rm --network "$N" "$IMG" $BENCH -h "$H" -q -n 2000 -c 10 -t set,get 2>&1 | tr '\r' '\n' | grep -c 'requests per second')
    ok "$([ "$b" = 2 ]; echo $?)" "10 concurrent clients, 2000 SET + 2000 GET"
    mem=$(cli info memory | tr -d '\r' | sed -n 's/^used_memory_human://p')
    note "server used_memory $mem"
    cli set persisted yes >/dev/null
    dk stop -t 20 "$H" >/dev/null
    ok "$([ "$(exit_code "$H")" = 0 ]; echo $?)" "docker stop: clean exit 0"
    dk logs "$H" 2>&1 | grep -qi "ready to exit"; ok $? "shutdown logged (ready to exit)"
    dk start "$H" >/dev/null
    wait_for 60 sh -c "[ \"\$(dk run --rm --network $N $IMG $CLI -h $H get persisted)\" = yes ]"
    ok $? "append-only file restores data after restart"
    logs_on_fail kv
}
