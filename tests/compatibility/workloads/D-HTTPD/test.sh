# D-HTTPD: Apache httpd 2.4 (event MPM, child processes): GET, HEAD, POST (200),
# concurrent requests, graceful restart, stop. Client: busybox wget.
# timeout: 240
IMG=httpd:2.4-alpine
dk pull -q $IMG >/dev/null; dk pull -q alpine:3.22 >/dev/null
setup
run_bg web $IMG sh -c 'echo httpd-ok > /usr/local/apache2/htdocs/index.html && exec httpd-foreground'
W=$(name web)
get() { dk run --rm --network "$N" alpine:3.22 wget -q -O - -T 10 "$@" 2>&1; }
wait_for 30 sh -c "dk run --rm --network $N alpine:3.22 wget -q -O /dev/null -T 3 http://$W/"
ok $? "serves on port 80"
ok "$([ "$(get http://$W/)" = httpd-ok ]; echo $?)" "GET static page"
ok "$(dk run --rm --network "$N" alpine:3.22 wget -S --spider -T 10 http://$W/ 2>&1 | grep -q 'HTTP/1.1 200'; echo $?)" "HEAD request"
ok "$(dk run --rm --network "$N" alpine:3.22 wget -S -O /dev/null -T 10 --post-data=a=b http://$W/ 2>&1 | grep -q 'HTTP/1.1 200'; echo $?)" "POST to a static file is answered (Apache's default handler: 200)"
n=$(dk run --rm --network "$N" alpine:3.22 sh -c "i=0; while [ \$i -lt 40 ]; do wget -q -O - -T 10 http://$W/ & i=\$((i+1)); done; wait" | grep -c httpd-ok)
ok "$([ "$n" = 40 ]; echo $?)" "40 concurrent requests ($n ok)"
note "processes in the container: $(x web sh -c 'ls /proc | grep -c "^[0-9]"')"
x web httpd -k graceful >/dev/null 2>&1; sleep 2
ok "$([ "$(get http://$W/)" = httpd-ok ]; echo $?)" "graceful restart keeps serving"
dk stop -t 10 "$W" >/dev/null
ok "$([ "$(exit_code "$W")" = 0 ]; echo $?)" "stop (SIGWINCH graceful-stop), exit 0"
logs_on_fail web
no_leftovers
