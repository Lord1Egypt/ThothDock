# F-PYTHON: CPython 3.13 on musl: threads, subprocess, pipes, signals, files,
# DNS, HTTPS, multiprocessing; http.server reached from another container
# and stopped with SIGTERM.
# timeout: 240
IMG=python:3.13-alpine
dk pull -q $IMG >/dev/null
setup
dk run -i --rm --network "$N" $IMG python - < fixture.py > "$TMPD/fixture.out" 2>&1
cat "$TMPD/fixture.out"
run_bg srv $IMG sh -c 'mkdir -p /srv && echo py-ok > /srv/index.html && cd /srv && exec python -m http.server 8000'
S=$(name srv)
wait_for 30 sh -c "dk run --rm --network $N $IMG python -c \"import urllib.request; urllib.request.urlopen('http://$S:8000/', timeout=3)\""
ok $? "http.server reached from another container"
dk stop -t 10 "$S" >/dev/null
ok "$(c=$(exit_code "$S"); [ "$c" = 143 ] || [ "$c" = 0 ]; echo $?)" "SIGTERM ends the server (exit $(exit_code "$S"))"
logs_on_fail srv
no_leftovers
