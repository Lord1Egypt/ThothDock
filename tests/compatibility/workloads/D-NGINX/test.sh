# D-NGINX: nginx 1.28 (master + workers, epoll, sendfile): static GET, POST,
# keep-alive, reverse proxy, reload, access log, 50 concurrent requests,
# a loopback-published port. Clients use the image's own curl.
# timeout: 240
IMG=nginx:1.28-alpine
dk pull -q $IMG >/dev/null
setup
run_bg web $IMG sh -c 'echo nginx-ok > /usr/share/nginx/html/index.html && exec nginx -g "daemon off;"'
W=$(name web)
c() { dk run --rm --network "$N" $IMG curl -s --max-time 10 "$@"; }
wait_for 30 sh -c "dk run --rm --network $N $IMG curl -sf --max-time 3 http://$W/ >/dev/null"
ok $? "serves on port 80 (unprivileged app: low port mapped by --net-ip)"
ok "$([ "$(c http://$W/)" = nginx-ok ]; echo $?)" "GET static page"
ok "$([ "$(c -o /dev/null -w '%{http_code}' -X POST -d a=b http://$W/)" = 405 ]; echo $?)" "POST to a static file is refused with 405"
ok "$([ "$(c -o /dev/null -w '%{http_version}' http://$W/)" = 1.1 ]; echo $?)" "HTTP/1.1"
reuse=$(dk run --rm --network "$N" $IMG curl -sv http://$W/ http://$W/ 2>&1 | grep -ciE 're-?using existing')
ok "$([ "$reuse" -ge 1 ]; echo $?)" "keep-alive: the second request reuses the connection"
run_bg proxy $IMG sh -c "printf 'server { listen 28081; location / { proxy_pass http://$W; } }' > /etc/nginx/conf.d/default.conf && exec nginx -g 'daemon off;'"
P=$(name proxy)
wait_for 30 sh -c "dk run --rm --network $N $IMG curl -sf --max-time 3 http://$P:28081/ >/dev/null"
ok "$([ "$(c http://$P:28081/)" = nginx-ok ]; echo $?)" "reverse proxy to another container"
n=$(dk run --rm --network "$N" $IMG sh -c "i=0; while [ \$i -lt 50 ]; do curl -s --max-time 10 http://$W/ & i=\$((i+1)); done; wait" | grep -c nginx-ok)
ok "$([ "$n" = 50 ]; echo $?)" "50 concurrent requests ($n ok)"
x web sh -c 'echo reloaded > /usr/share/nginx/html/index.html; nginx -s reload' >/dev/null 2>&1
sleep 1
ok "$([ "$(c http://$W/)" = reloaded ]; echo $?)" "nginx -s reload keeps serving"
ok "$([ "$(dk logs $W 2>/dev/null | grep -c '"GET / HTTP/1.1" 200')" -ge 50 ]; echo $?)" "access log reaches docker logs"
port=$(( 20000 + $(date +%s) % 20000 ))
run_bg pub -p 127.0.0.1:$port:80 $IMG sh -c 'echo published > /usr/share/nginx/html/index.html && exec nginx -g "daemon off;"'
wait_for 30 sh -c "dk run --rm $IMG curl -sf --max-time 3 http://127.0.0.1:$port/ | grep -q published"
ok $? "published on 127.0.0.1:$port only"
dk stop -t 10 "$W" >/dev/null
ok "$([ "$(exit_code "$W")" = 0 ]; echo $?)" "graceful stop (SIGQUIT), exit 0"
logs_on_fail web
no_leftovers
