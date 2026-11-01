# D-CADDY: Caddy 2 (Go): HTTPS with its internal CA, HTTP/2, reverse proxy to
# a BusyBox httpd backend, config reload, stop.
# timeout: 240
IMG=caddy:2-alpine
dk pull -q $IMG >/dev/null; dk pull -q busybox:1.37 >/dev/null; dk pull -q nginx:1.28-alpine >/dev/null
setup
run_bg backend busybox:1.37 sh -c 'mkdir -p /www && echo backend-ok > /www/index.html && exec httpd -f -p 8000 -h /www'
B=$(name backend); C=$(name caddy)
run_bg caddy $IMG sh -c "printf '{\n auto_https disable_redirects\n skip_install_trust\n http_port 8080\n https_port 8443\n}\nhttps://$C:8443 {\n tls internal\n respond \"caddy-tls-ok\"\n}\nhttp://:8081 {\n reverse_proxy $B:8000\n}\n' > /etc/caddy/Caddyfile && exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"
c() { dk run --rm --network "$N" nginx:1.28-alpine curl -s --max-time 10 "$@"; }
wait_for 60 sh -c "dk run --rm --network $N nginx:1.28-alpine curl -sfk --max-time 3 https://$C:8443/ >/dev/null"
ok $? "HTTPS with the internal CA"
ok "$([ "$(c -k https://$C:8443/)" = caddy-tls-ok ]; echo $?)" "TLS response body"
ok "$([ "$(c -k -o /dev/null -w '%{http_version}' https://$C:8443/)" = 2 ]; echo $?)" "HTTP/2 negotiated over TLS (ALPN)"
ok "$([ "$(c http://$C:8081/)" = backend-ok ]; echo $?)" "reverse proxy to another container"
x caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1
ok "$([ "$(c -k https://$C:8443/)" = caddy-tls-ok ]; echo $?)" "caddy reload keeps serving"
dk stop -t 10 "$C" >/dev/null
ok "$([ "$(exit_code "$C")" = 0 ]; echo $?)" "graceful stop, exit 0"
logs_on_fail caddy
no_leftovers
