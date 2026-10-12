# F-GO: a static Go program (64 goroutines, os/exec, pipes, signals, files,
# DNS, HTTPS, net/http) in Alpine, and as a server reached from another
# container that exits 0 on SIGTERM.
# timeout: 180
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
probe=$COMPAT_PROBES/go-probe
[ -x "$probe" ] || { echo "BLOCKED no $probe for this architecture"; return 0; }
dk run -i --rm --network "$N" $IMG sh -c 'cat > /tmp/g && chmod 700 /tmp/g && /tmp/g' < "$probe" > "$TMPD/go.out" 2>&1
code=$?; cat "$TMPD/go.out"; ok $code "go probe exits 0"
# docker run -d never streams stdin (as in Docker): stage the binary in a
# volume, then run it as the container's PID 1.
V=$(name bin); dk volume create "$V" >/dev/null
dk run -i --rm -v "$V":/v $IMG sh -c 'cat > /v/g && chmod 755 /v/g' < "$probe"
dk run -d --restart no --network "$N" --name "$(name srv)" -v "$V":/v $IMG /v/g serve >/dev/null
S=$(name srv)
wait_for 30 sh -c "[ \"\$(dk run --rm --network $N $IMG wget -q -O - http://$S:8000/)\" = go-ok ]"
ok $? "Go HTTP server reached from another container"
dk stop -t 10 "$S" >/dev/null
ok "$([ "$(exit_code "$S")" = 0 ]; echo $?)" "SIGTERM handled, exit 0"
no_leftovers
