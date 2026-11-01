# P-IPC: System V shared memory, semaphores and message queues (PRoot
# --sysvipc) inside an Alpine container, then the same probe twice at once
# in two containers: each must see only its own IPC objects.
# timeout: 180
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
probe=$COMPAT_PROBES/ipc-probe
[ -x "$probe" ] || { echo "BLOCKED no $probe for this architecture"; return 0; }
dk run -i --rm --network "$N" --name "$(name ipc)" $IMG \
    sh -c 'cat > /tmp/ipc-probe && chmod 700 /tmp/ipc-probe && /tmp/ipc-probe all' < "$probe" > "$TMPD/ipc.out" 2>&1
code=$?
cat "$TMPD/ipc.out"
ok $code "ipc probe exits 0"
# Two containers create the same key at the same time: with one IPC
# namespace per container, both IPC_EXCL creates succeed.
# (Attached runs: docker run -d never streams stdin, as in Docker.)
for c in a b; do
    dk run -i --rm --network "$N" --name "$(name $c)" $IMG \
        sh -c 'cat > /tmp/p && chmod 700 /tmp/p && /tmp/p postgres' < "$probe" > "$TMPD/ns-$c.out" 2>&1 &
done
wait
ok "$(grep -q '^PASS postgres: shmget' "$TMPD/ns-a.out" && grep -q '^PASS postgres: shmget' "$TMPD/ns-b.out" && ! grep -q FAIL "$TMPD/ns-a.out" "$TMPD/ns-b.out"; echo $?)" \
    "same key created at once in two containers: separate IPC namespaces"
rm -f "$TMPD/ipc.out"
no_leftovers
