# I-NESTED: ThothDock inside a ThothDock container (PRoot under PRoot): a
# static engine and a static PRoot from COMPAT_PROBES run in docker:cli and
# start a nested busybox container. Exploratory (P2).
# timeout: 420
IMG=docker:29-cli
dk pull -q $IMG >/dev/null
setup
for f in thothdock proot; do
    [ -x "$COMPAT_PROBES/$f" ] || { echo "BLOCKED no static $f in $COMPAT_PROBES for this architecture"; return 0; }
done
( cd "$COMPAT_PROBES" && tar cf - thothdock proot ) | dk run -i --rm --network "$N" --name "$(name nest)" $IMG sh -c '
    mkdir -p /opt/n && tar xf - -C /opt/n || exit 5
    /opt/n/thothdock serve --root /tmp/td --socket /tmp/td.sock --proot /opt/n/proot > /tmp/td.log 2>&1 &
    i=0; while [ ! -S /tmp/td.sock ] && [ $i -lt 100 ]; do sleep 0.2; i=$((i+1)); done
    export DOCKER_HOST=unix:///tmp/td.sock
    docker version --format "inner engine {{.Server.Platform.Name}} {{.Server.Version}}" || { tail -5 /tmp/td.log; exit 3; }
    docker pull -q busybox:1.37 >/dev/null || { echo pull failed; tail -5 /tmp/td.log; exit 4; }
    docker run --rm busybox:1.37 echo nested-ok
    code=$?; [ $code = 0 ] || tail -8 /tmp/td.log
    exit $code' > "$TMPD/nested.out" 2>&1
code=$?
sed 's/^/    | /' "$TMPD/nested.out"
ok "$(grep -q 'inner engine ThothDock' "$TMPD/nested.out"; echo $?)" "inner engine starts and answers"
ok "$([ $code = 0 ] && grep -q '^nested-ok$' "$TMPD/nested.out"; echo $?)" "a container runs inside the container (PRoot under PRoot)"
no_leftovers
