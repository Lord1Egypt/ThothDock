# A-POSTGRES: PostgreSQL 17 (System V shared memory interlock and dynamic
# shared memory, process-per-connection, fsync, WAL): init, SQL, index,
# transactions, parallel query, concurrent clients, clean restart, crash
# recovery. dynamic_shared_memory_type=sysv is what initdb picks on Android
# (no /dev/shm); forcing it here makes hosts take the same path.
# timeout: 420
IMG=postgres:17-alpine
dk pull -q $IMG >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
run_bg pg -e POSTGRES_USER=thoth -e POSTGRES_PASSWORD=disposable-test-only -e POSTGRES_DB=obs -v "$V":/var/lib/postgresql/data \
    $IMG postgres -c dynamic_shared_memory_type=sysv
P=$(name pg)
sql() { dk exec -i -e PGPASSWORD=disposable-test-only "$P" psql -X -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U thoth -d obs -At "$@"; }
ready() { wait_for 180 sh -c "dk exec $P pg_isready -h 127.0.0.1 -U thoth -d obs && dk exec -e PGPASSWORD=disposable-test-only $P psql -h 127.0.0.1 -U thoth -d obs -Atc 'select 1'"; }
ready; ok $? "initdb, start, pg_isready and a query"
[ "$fails" = 0 ] || { logs_on_fail pg; return 0; }
note "$(dk logs "$P" 2>&1 | grep -m1 'selecting dynamic shared memory')"
# docker exec runs in another PRoot; psql's default is the Unix socket in
# the container's rootfs (a host path longer than sun_path).
ok "$([ "$(dk exec "$P" psql -U thoth -d obs -Atc 'select 1' 2>&1)" = 1 ]; echo $?)" "docker exec psql over the Unix socket"
sql -c "create table obs (id serial primary key, site text not null, val numeric not null)"; ok $? "create table"
sql -c "insert into obs (site, val) select 's' || (g % 7), g from generate_series(1, 50000) g"; ok $? "insert 50000 rows"
ok "$([ "$(sql -c 'select count(*), sum(val) from obs')" = "50000|1250025000" ]; echo $?)" "select count and sum"
sql -c "create index on obs (site)" && sql -c "update obs set val = val + 1 where site = 's3'" && sql -c "delete from obs where site = 's6'"
ok "$([ "$(sql -c "select count(*) from obs where site = 's6'")" = 0 ]; echo $?)" "index, update, delete"
sql -c "begin; insert into obs (site, val) values ('rb', 1); rollback;"; sql -c "begin; insert into obs (site, val) values ('cm', 2); commit;"
ok "$([ "$(sql -c "select count(*) filter (where site='rb'), count(*) filter (where site='cm') from obs")" = "0|1" ]; echo $?)" "rollback and commit"
plan=$(sql -c "set debug_parallel_query = on; set parallel_setup_cost = 0; set min_parallel_table_scan_size = 0; explain (analyze, costs off, timing off) select site, count(*) from obs group by site")
ok "$(echo "$plan" | grep -q 'Workers Launched: [1-9]'; echo $?)" "parallel query workers (System V dynamic shared memory)"
for i in 1 2 3 4; do sql -c "insert into obs (site, val) select 'cl$i', g from generate_series(1, 500) g" & done; wait
ok "$([ "$(sql -c "select count(*) from obs where site like 'cl_'")" = 2000 ]; echo $?)" "4 concurrent clients"
n=$(sql -c 'select count(*) from obs')
dk stop -t 30 "$P" >/dev/null
ok "$([ "$(exit_code "$P")" = 0 ] && dk logs --tail 3 "$P" 2>&1 | grep -q 'database system is shut down'; echo $?)" "clean shutdown"
dk start "$P" >/dev/null; ready
ok "$([ "$(sql -c 'select count(*) from obs')" = "$n" ]; echo $?)" "restart keeps $n rows"
dk kill "$P" >/dev/null; dk start "$P" >/dev/null; ready
ok "$([ "$(sql -c 'select count(*) from obs')" = "$n" ] && dk logs --tail 30 "$P" 2>&1 | grep -qE 'not properly shut down|was interrupted'; echo $?)" "crash recovery after SIGKILL keeps the data"
logs_on_fail pg
no_leftovers
