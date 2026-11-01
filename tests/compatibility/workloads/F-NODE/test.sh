# F-NODE: Node.js 22 (libuv, V8 JIT, worker threads): child processes, pipes,
# signals, files, DNS, HTTPS; an HTTP server reached from another container
# that exits 0 on SIGTERM.
# timeout: 240
IMG=node:22-alpine
dk pull -q $IMG >/dev/null
setup
dk run -i --rm --network "$N" $IMG node - < fixture.js > "$TMPD/fixture.out" 2>&1
cat "$TMPD/fixture.out"
run_bg srv $IMG node -e "const s=require('http').createServer((q,r)=>r.end('node-ok')).listen(3000); process.on('SIGTERM',()=>s.close(()=>process.exit(0)))"
S=$(name srv)
wait_for 30 sh -c "[ \"\$(dk run --rm --network $N $IMG wget -q -O - http://$S:3000/)\" = node-ok ]"
ok $? "HTTP server reached from another container"
dk stop -t 10 "$S" >/dev/null
ok "$([ "$(exit_code "$S")" = 0 ]; echo $?)" "SIGTERM handler closes the server, exit 0"
logs_on_fail srv
no_leftovers
