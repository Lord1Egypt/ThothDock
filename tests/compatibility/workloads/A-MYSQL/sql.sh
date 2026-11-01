# mysql_family IMAGE CLIENT ADMIN: MySQL-protocol server (MySQL, MariaDB).
mysql_family() {
    IMG=$1 CLIENT=$2 ADMIN=$3
    dk pull -q "$IMG" >/dev/null || { ok 1 "pull $IMG"; return; }
    setup
    V=$(name data); dk volume create "$V" >/dev/null
    run_bg db -e MYSQL_ROOT_PASSWORD=disposable-test-only -e MARIADB_ROOT_PASSWORD=disposable-test-only \
        -e MYSQL_DATABASE=obs -e MARIADB_DATABASE=obs -v "$V":/var/lib/mysql "$IMG"
    D=$(name db)
    sql() { dk exec -i "$D" $CLIENT -uroot -pdisposable-test-only -h 127.0.0.1 --protocol=TCP -N -B obs -e "$1" 2>/dev/null; }
    ready() { wait_for 240 sh -c "dk exec $D $CLIENT -uroot -pdisposable-test-only -h 127.0.0.1 --protocol=TCP -N -B obs -e 'select 1' 2>/dev/null | grep -q '^1$'"; }
    ready; ok $? "initialization, start and an authenticated TCP query"
    [ "$fails" = 0 ] || { logs_on_fail db; return 0; }
    note "server $(sql 'select version()') aio=$(sql "select @@innodb_use_native_aio")"
    ok "$([ "$(dk exec "$D" $CLIENT -uroot -pdisposable-test-only -N -B -e 'select 1' 2>/dev/null)" = 1 ]; echo $?)" "docker exec client over the Unix socket"
    bad=$(dk exec "$D" $CLIENT -uroot -pwrong -h 127.0.0.1 --protocol=TCP -e 'select 1' 2>&1 | grep -c 'Access denied')
    ok "$([ "$bad" = 1 ]; echo $?)" "a wrong password is refused"
    sql "create table obs (id int auto_increment primary key, site varchar(8) not null, val int not null) engine=InnoDB"; ok $? "create table (InnoDB)"
    # Recursive CTEs stop at 1000 iterations by default.
    limit="set session cte_max_recursion_depth = 30000;"
    [ "$CLIENT" = mariadb ] && limit="set session max_recursive_iterations = 30000;"
    sql "$limit insert into obs (site, val) with recursive g(n) as (select 1 union all select n + 1 from g where n < 20000) select concat('s', n % 7), n from g"
    ok "$([ "$(sql 'select count(*), sum(val) from obs' | tr '\t' '|')" = "20000|200010000" ]; echo $?)" "insert 20000 rows and read back"
    sql "create index i_site on obs (site)" && sql "update obs set val = val + 1 where site = 's3'" && sql "delete from obs where site = 's6'"
    ok "$([ "$(sql "select count(*) from obs where site = 's6'")" = 0 ]; echo $?)" "index, update, delete"
    sql "start transaction; insert into obs (site, val) values ('rb', 1); rollback;"; sql "start transaction; insert into obs (site, val) values ('cm', 2); commit;"
    ok "$([ "$(sql "select sum(site='rb'), sum(site='cm') from obs" | tr '\t' '|')" = "0|1" ]; echo $?)" "rollback and commit"
    for i in 1 2 3 4; do sql "insert into obs (site, val) values ('cl$i', 1), ('cl$i', 2), ('cl$i', 3)" & done; wait
    ok "$([ "$(sql "select count(*) from obs where site like 'cl_'")" = 12 ]; echo $?)" "4 concurrent clients"
    n=$(sql 'select count(*) from obs')
    dk stop -t 60 "$D" >/dev/null
    ok "$([ "$(exit_code "$D")" = 0 ]; echo $?)" "docker stop: clean exit 0"
    dk start "$D" >/dev/null; ready
    ok "$([ "$(sql 'select count(*) from obs')" = "$n" ]; echo $?)" "restart keeps $n rows"
    dk kill "$D" >/dev/null; dk start "$D" >/dev/null; ready
    ok "$([ "$(sql 'select count(*) from obs')" = "$n" ]; echo $?)" "InnoDB crash recovery after SIGKILL keeps the data"
    logs_on_fail db
}
