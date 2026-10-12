# minimal_env IMAGE: shell-level container semantics every image needs.
# Sourced by G-ALPINE, G-DEBIAN and G-BUSYBOX.
minimal_env() {
    IMG=$1
    dk pull -q "$IMG" >/dev/null || { ok 1 "pull $IMG"; return; }
    setup
    [ "$(dk run --rm --network "$N" "$IMG" sh -c 'echo out; echo err >&2' 2>/dev/null)" = out ]
    ok $? "stdout is separate from stderr"
    dk run --rm --network "$N" "$IMG" sh -c 'exit 42'; ok "$([ $? = 42 ]; echo $?)" "exit code 42 propagates"
    [ "$(dk run --rm --network "$N" -e FOO=bar -w /tmp "$IMG" sh -c 'echo $FOO $PWD')" = "bar /tmp" ]
    ok $? "environment variable and working directory"
    [ "$(echo hello | dk run -i --rm --network "$N" "$IMG" sh -c 'read l; echo got:$l')" = got:hello ]
    ok $? "stdin reaches the container (-i)"
    tty_out=$(dk run -t --rm --network "$N" "$IMG" sh -c 'test -t 1 && echo tty' 2>/dev/null | tr -d '\r')
    ok "$([ "$tty_out" = tty ]; echo $?)" "-t gives the process a terminal"
    [ "$(dk run --rm --network "$N" "$IMG" sh -c 'i=0; while [ $i -lt 200 ]; do (true); i=$((i+1)); done; echo $i')" = 200 ]
    ok $? "200 subshell forks"
    [ "$(dk run --rm --network "$N" "$IMG" sh -c 'sleep 1 & a=$!; sleep 1 & b=$!; wait $a $b; echo done')" = done ]
    ok $? "background jobs and wait"
    perm=$(dk run --rm --network "$N" "$IMG" sh -c 'cd /tmp && umask 022 && echo x > f && chmod 640 f && ln f h && ln -s f s && ls -ln f | cut -c1-12; readlink s; cat h; mkdir -p d/e && rm -r d && echo rmok')
    ok "$([ "$(echo "$perm" | sed -n 1p | cut -c1-10)" = "-rw-r-----" ] && echo "$perm" | grep -q '^f$' && echo "$perm" | grep -q '^x$' && echo "$perm" | grep -q rmok; echo $?)" \
        "permissions, hard link, symlink, temporary files"
    # Signals: SIGUSR1 reaches a trap; docker stop's SIGTERM ends a trapping shell.
    run_bg sig "$IMG" sh -c 'trap "echo usr1 > /tmp/u" USR1; trap "exit 143" TERM; while :; do sleep 0.2; done'
    wait_for 15 running "$(name sig)"
    dk kill -s USR1 "$(name sig)" >/dev/null
    wait_for 10 x sig test -f /tmp/u; ok $? "docker kill -s USR1 reaches the trap"
    ok "$([ "$(x sig cat /tmp/u)" = usr1 ]; echo $?)" "docker exec sees the container's files"
    dk stop -t 10 "$(name sig)" >/dev/null
    ok "$([ "$(exit_code "$(name sig)")" = 143 ]; echo $?)" "docker stop: SIGTERM handled, exit 143"
    V=$(name vol)
    dk volume create "$V" >/dev/null
    dk run --rm --network "$N" -v "$V":/data "$IMG" sh -c 'echo kept > /data/f'
    ok "$([ "$(dk run --rm --network "$N" -v "$V":/data "$IMG" cat /data/f)" = kept ]; echo $?)" "named volume persists between containers"
    dns=$(dk run --rm --network "$N" "$IMG" sh -c 'getent hosts dns.google 2>/dev/null || nslookup dns.google 2>/dev/null' | grep -cE '8\.8\.|2001:4860')
    ok "$([ "$dns" -gt 0 ]; echo $?)" "DNS lookup of dns.google"
}
