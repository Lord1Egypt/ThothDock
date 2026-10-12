# P-PROMETHEUS: Prometheus (Go; TSDB WAL, mmap'd chunks, fsync, many
# goroutines) scraping a local metrics endpoint and itself, queries,
# persistence after a clean restart, and a file-descriptor / thread check.
# timeout: 360
IMG=prom/prometheus:v3.5.0; CLI=alpine:3.22
dk pull -q $IMG >/dev/null; dk pull -q busybox:1.37 >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
C=$(name conf); dk volume create "$C" >/dev/null
run_bg tgt busybox:1.37 sh -c 'mkdir -p /www && printf "# TYPE compat_demo_total counter\ncompat_demo_total 42\n# TYPE compat_demo_gauge gauge\ncompat_demo_gauge 7.5\n" > /www/metrics && exec httpd -f -p 9100 -h /www'
T=$(name tgt)
dk run --rm -v "$C":/c busybox:1.37 sh -c "printf 'global:\n  scrape_interval: 1s\nscrape_configs:\n  - job_name: demo\n    static_configs:\n      - targets: [\"$T:9100\"]\n  - job_name: self\n    static_configs:\n      - targets: [\"localhost:9090\"]\n' > /c/prometheus.yml"
run_bg prom -v "$V":/prometheus -v "$C":/c $IMG --config.file=/c/prometheus.yml --storage.tsdb.path=/prometheus --storage.tsdb.retention.time=1d
P=$(name prom)
q() { dk run --rm --network "$N" busybox:1.37 wget -q -O - -T 10 "http://$P:9090/api/v1/query?query=$1"; }
wait_for 90 sh -c "dk run --rm --network $N busybox:1.37 wget -q -O - -T 3 http://$P:9090/-/ready | grep -qi ready"
ok $? "Prometheus is ready"
wait_for 60 sh -c "dk run --rm --network $N busybox:1.37 wget -q -O - -T 3 'http://$P:9090/api/v1/query?query=compat_demo_gauge' | grep -q '7.5'"
ok $? "the demo target was scraped and queried (compat_demo_gauge = 7.5)"
ok "$(q 'up%7Bjob%3D%22self%22%7D' | grep -q '"1"'; echo $?)" "self-scrape: up{job=self} = 1"
ok "$(q 'sum(count_over_time(compat_demo_total%5B20s%5D))' | grep -q '"status":"success"'; echo $?)" "range function over the TSDB head"
pidof_prom() { dk run --rm busybox:1.37 sh -c 'for d in /proc/[0-9]*; do [ "$(cat $d/comm 2>/dev/null)" = prometheus ] && echo ${d#/proc/} && break; done'; }
PID=$(pidof_prom)
fds() { dk run --rm busybox:1.37 sh -c "ls /proc/$PID/fd | wc -l"; }
thr() { dk run --rm busybox:1.37 sh -c "ls /proc/$PID/task | wc -l"; }
f1=$(fds); t1=$(thr); sleep 30; f2=$(fds); t2=$(thr)
note "prometheus pid $PID: fds $f1 -> $f2, threads $t1 -> $t2 over 30 s"
ok "$([ "$f2" -le $((f1 + 5)) ] && [ "$t2" -le $((t1 + 8)) ]; echo $?)" "no descriptor or thread growth while idle scraping"
cs1=$(dk run --rm busybox:1.37 sh -c "grep -E '^(voluntary|nonvoluntary)_ctxt' /proc/$PID/status | awk '{s+=\$2} END {print s}'"); sleep 20
cs2=$(dk run --rm busybox:1.37 sh -c "grep -E '^(voluntary|nonvoluntary)_ctxt' /proc/$PID/status | awk '{s+=\$2} END {print s}'")
note "context switches of the main thread over 20 s (1 s scrape interval): $((cs2 - cs1))"
dk stop -t 60 "$P" >/dev/null
ok "$(c=$(exit_code "$P"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "clean stop flushes the WAL (exit $(exit_code "$P"))"
dk start "$P" >/dev/null
wait_for 120 sh -c "dk run --rm --network $N busybox:1.37 wget -q -O - -T 3 http://$P:9090/-/ready | grep -qi ready"
ok "$(q 'count_over_time(compat_demo_total%5B10m%5D)' | grep -q '"status":"success"' && [ "$(q 'count_over_time(compat_demo_total%5B10m%5D)' | sed -n 's/.*"value":\[[0-9.]*,"\([0-9]*\)".*/\1/p' | head -1)" -gt 20 ]; echo $?)" "samples scraped before the restart are still queryable (WAL replay)"
dk kill "$P" >/dev/null; dk start "$P" >/dev/null
wait_for 120 sh -c "dk run --rm --network $N busybox:1.37 wget -q -O - -T 3 http://$P:9090/-/ready | grep -qi ready"
ok $? "starts again after SIGKILL (WAL recovery)"
logs_on_fail prom
no_leftovers
