# L-FLUENTBIT: Fluent Bit (C; inotify tail, rotation, truncation, HTTP
# output): it follows a log file on a volume, survives rotation and
# truncation, and delivers parsed events to a local receiver. Reports its
# idle wake-ups; one flush tick per second is expected, a busy loop is not.
# timeout: 240
IMG=fluent/fluent-bit:4.0; CLI=alpine:3.22
dk pull -q $IMG >/dev/null; dk pull -q $CLI >/dev/null
setup
V=$(name logs); dk volume create "$V" >/dev/null
dk run --rm -v "$V":/v $CLI sh -c 'mkdir -p /v/in && : > /v/in/app.log && printf "[SERVICE]\n    Flush 1\n    Log_Level info\n[INPUT]\n    Name tail\n    Path /v/in/app.log\n    Tag app\n    Refresh_Interval 1\n    DB /v/tail.db\n    Parser kv\n[OUTPUT]\n    Name http\n    Match app\n    Host recv\n    Port 9880\n    URI /ingest\n    Format json_lines\n" > /v/fb.conf && printf "[PARSER]\n    Name kv\n    Format regex\n    Regex ^(?<level>[A-Z]+) (?<msg>.*)$\n" > /v/parsers.conf'
run_bg recv --network-alias recv -v "$V":/v $CLI sh -c 'while true; do nc -l -p 9880 >> /v/recv.raw; done'
run_bg fb -v "$V":/v $IMG /fluent-bit/bin/fluent-bit -c /v/fb.conf -R /v/parsers.conf
F=$(name fb)
sleep 5
append() { dk run --rm -v "$V":/v $CLI sh -c "echo '$1' >> /v/in/app.log"; }
got() { dk run --rm -v "$V":/v $CLI sh -c "grep -c '$1' /v/recv.raw"; }
append "INFO started"; append "WARN disk 91 percent"; append "ERROR failed job 7"
wait_for 30 sh -c "[ \"\$(dk run --rm -v $V:/v $CLI sh -c \"grep -c 'failed job 7' /v/recv.raw\")\" -ge 1 ]"
ok $? "appended lines are detected and delivered to the receiver"
ok "$(dk run --rm -v "$V":/v $CLI sh -c "grep -q '\"level\":\"ERROR\"' /v/recv.raw && grep -q '\"msg\":\"failed job 7\"' /v/recv.raw"; echo $?)" "the regex parser split level and message"
dk run --rm -v "$V":/v $CLI sh -c 'mv /v/in/app.log /v/in/app.log.1 && : > /v/in/app.log'
sleep 2; append "INFO after rotation"
wait_for 30 sh -c "[ \"\$(dk run --rm -v $V:/v $CLI sh -c \"grep -c 'after rotation' /v/recv.raw\")\" -ge 1 ]"
ok $? "a rotated file is followed (new file picked up)"
dk run --rm -v "$V":/v $CLI sh -c ': > /v/in/app.log'; sleep 2; append "INFO after truncate"
wait_for 30 sh -c "[ \"\$(dk run --rm -v $V:/v $CLI sh -c \"grep -c 'after truncate' /v/recv.raw\")\" -ge 1 ]"
ok $? "a truncated file is followed"
PID=$(dk run --rm $CLI sh -c 'for d in /proc/[0-9]*; do case "$(cat $d/comm 2>/dev/null)" in fluent-bit) echo ${d#/proc/}; break;; esac; done')
cs() { dk run --rm $CLI sh -c "cat /proc/$PID/task/*/status | grep -E '^(voluntary|nonvoluntary)_ctxt' | awk '{s+=\$2} END {print s}'"; }
a=$(cs); sleep 20; b=$(cs)
note "fluent-bit pid $PID: $((b - a)) context switches in 20 s idle (all threads)"
ok "$([ $(( (b - a) / 20 )) -lt 200 ]; echo $?)" "no busy loop while idle ($(( (b - a) / 20 )) switches per second across all threads)"
dk stop -t 20 "$F" >/dev/null
ok "$(c=$(exit_code "$F"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "stop flushes and exits (exit $(exit_code "$F"))"
dk start "$F" >/dev/null; sleep 5; append "INFO after restart"
wait_for 30 sh -c "[ \"\$(dk run --rm -v $V:/v $CLI sh -c \"grep -c 'after restart' /v/recv.raw\")\" -ge 1 ]"
ok $? "after a restart only new lines are delivered (offset database on the volume)"
ok "$([ "$(got 'started')" -le 2 ]; echo $?)" "the first lines were not delivered again after the restart ($(got 'started') occurrences)"
logs_on_fail fb
no_leftovers
