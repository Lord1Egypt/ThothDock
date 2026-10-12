# P-UNIX: Unix sockets across separate PRoot instances and containers, on a
# path that cannot fit sun_path (Garden patch 0014): server, docker exec
# client, a second container on a shared volume, server restart, competing
# bind, leftover aliases. The socket sits in a directory nested so deep that
# its host path is over 200 bytes whatever the engine root is.
# timeout: 240
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
probe=$COMPAT_PROBES/unix-probe
[ -x "$probe" ] || { echo "BLOCKED no $probe for this architecture"; return 0; }
V=$(name sock); dk volume create "$V" >/dev/null
D=d01234567890123456789012345678901234567890123456789012345678
dk run -i --rm -v "$V":/v $IMG sh -c "mkdir -p /v/$D && cat > /v/unix-probe && chmod 755 /v/unix-probe" < "$probe"
S=/v/$D/s.sock
hostpath=$(dk volume inspect -f '{{.Mountpoint}}' "$V")/_data/$D/s.sock
note "socket host path is ${#hostpath} bytes (sun_path holds 108)"
ok "$([ ${#hostpath} -gt 108 ]; echo $?)" "the host path does not fit sun_path"
run_bg srv -v "$V":/v $IMG /v/unix-probe loop $S
wait_for 30 dk exec "$(name srv)" test -S $S
ok $? "server created the socket (test -S)"
ok "$([ "$(dk exec "$(name srv)" /v/unix-probe connect $S)" = "pong peer=$S" ]; echo $?)" "docker exec client in the same container connects; peer name is the guest path"
dk run --rm -v "$V":/v $IMG /v/unix-probe connect $S > "$TMPD/c2.out"
ok "$([ "$(cat "$TMPD/c2.out")" = "pong peer=$S" ]; echo $?)" "a second container on the shared volume connects"
dk stop -t 10 "$(name srv)" >/dev/null
ok "$(dk run --rm -v "$V":/v $IMG sh -c "test ! -e $S"; echo $?)" "the socket is removed when the server stops"
dk start "$(name srv)" >/dev/null
wait_for 30 dk exec "$(name srv)" test -S $S
ok "$([ "$(dk run --rm -v "$V":/v $IMG /v/unix-probe connect $S)" = "pong peer=$S" ]; echo $?)" "after a server restart the recreated socket works"
out=$(dk run --rm -v "$V":/v $IMG /v/unix-probe serve $S 2>&1 < /dev/null)
ok "$(echo "$out" | grep -qi 'address already in use'; echo $?)" "a competing bind fails with EADDRINUSE"
ok "$([ "$(dk run --rm -v "$V":/v $IMG /v/unix-probe connect $S)" = "pong peer=$S" ]; echo $?)" "the first server is unaffected by the failed bind"
dk container remove --force "$(name srv)" >/dev/null
if [ -n "$COMPAT_ENGINE_ROOT" ] && [ -d "$COMPAT_ENGINE_ROOT/tmp/proot" ]; then
    c=$(find "$COMPAT_ENGINE_ROOT/tmp/proot" -name 'prootsock-c-*' | wc -l)
    ok "$([ "$c" = 0 ]; echo $?)" "no connect alias left in PROOT_TMP_DIR ($c)"
    for a in $(find "$COMPAT_ENGINE_ROOT/tmp/proot" -name 'prootsock-*' -type l); do
        [ -e "$a" ] || stale=1
    done
    note "dangling bind aliases are swept at the next long-path bind (stale now: ${stale:-0})"
fi
no_leftovers
