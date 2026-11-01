# P-CORE: the core Linux probe (process, memory, sync, fs, net) inside an
# Alpine container. The static probe arrives on stdin, so no bind mount or
# host path is involved.
# timeout: 180
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
probe=$COMPAT_PROBES/core-probe
[ -x "$probe" ] || { echo "BLOCKED no $probe for this architecture"; return 0; }
dk run -i --rm --network "$N" --name "$(name probe)" $IMG \
    sh -c 'cat > /tmp/core-probe && chmod 700 /tmp/core-probe && /tmp/core-probe all' < "$probe" > "$TMPD/probe.out" 2>&1
code=$?
cat "$TMPD/probe.out"
rm -f "$TMPD/probe.out"
ok $code "core probe exits 0"
no_leftovers
