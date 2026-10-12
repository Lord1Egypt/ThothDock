# D-PHPFPM: PHP-FPM behind nginx over FastCGI on TCP and on a Unix socket in
# a named volume (host path over sun_path, separate PRoot per container),
# worker pool, reload (SIGUSR2) and graceful stop (SIGQUIT).
# timeout: 300
FPM=php:8.4-fpm-alpine; NGX=nginx:1.28-alpine
dk pull -q $FPM >/dev/null; dk pull -q $NGX >/dev/null
setup
V=$(name run); dk volume create "$V" >/dev/null
W=$(name www); dk volume create "$W" >/dev/null
D=d01234567890123456789012345678901234567890123456789012345678
dk run --rm -v "$V":/v -v "$W":/www $NGX sh -c "mkdir -p /v/$D /www && echo '<?php echo \"php-ok:\" . php_sapi_name() . \":\" . getmypid() > 0; ' > /www/index.php && chmod -R a+rX /www /v && chmod 777 /v/$D"
SOCK=/v/$D/php.sock
run_bg fpm -v "$V":/v -v "$W":/www $FPM sh -c "printf '[www]\nuser=www-data\ngroup=www-data\nlisten=$SOCK\nlisten.mode=0666\npm=dynamic\npm.max_children=5\npm.start_servers=2\npm.min_spare_servers=1\npm.max_spare_servers=3\n' > /usr/local/etc/php-fpm.d/zz-test.conf && printf 'listen = 9000\n' > /dev/null && exec php-fpm -F"
F=$(name fpm)
wait_for 60 dk exec $F test -S $SOCK
ok $? "php-fpm created its FastCGI socket on the volume"
run_bg fpmtcp -v "$W":/www $FPM sh -c "printf '[www]\nlisten=9000\n' > /usr/local/etc/php-fpm.d/zz-test.conf && exec php-fpm -F"
T=$(name fpmtcp)
conf() { printf 'server { listen 80; root /www; location ~ \\.php$ { fastcgi_pass %s; include fastcgi_params; fastcgi_param SCRIPT_FILENAME /www$fastcgi_script_name; } }' "$1"; }
run_bg ngx -v "$V":/v -v "$W":/www $NGX sh -c "$(printf "printf '%s' > /etc/nginx/conf.d/default.conf" "$(conf unix:$SOCK)") && exec nginx -g 'daemon off;'"
N=$(name ngx)
run_bg ngxtcp -v "$W":/www $NGX sh -c "$(printf "printf '%s' > /etc/nginx/conf.d/default.conf" "$(conf $T:9000)") && exec nginx -g 'daemon off;'"
M=$(name ngxtcp)
get() { dk run --rm --network "$N" --network-alias x $NGX curl -s --max-time 10 "$@"; }
getn() { dk run --rm --network "$N" $NGX curl -s --max-time 10 -o /dev/null -w '%{http_code}' "$@"; }
wait_for 60 sh -c "[ \"\$(dk run --rm --network $N $NGX curl -s -o /dev/null -w '%{http_code}' http://$N/index.php)\" = 200 ]"
ok $? "FastCGI over the Unix socket: nginx (container B) reaches php-fpm (container A)"
ok "$([ "$(getn http://$M/index.php)" = 200 ]; echo $?)" "FastCGI over TCP"
body=$(get http://$N/index.php)
ok "$(echo "$body" | grep -q 'php-ok:fpm-fcgi'; echo $?)" "the PHP script ran in a worker ($body)"
n=$(dk run --rm --network "$N" $NGX sh -c "i=0; while [ \$i -lt 30 ]; do curl -s -o /dev/null -w '%{http_code}\n' http://$N/index.php & i=\$((i+1)); done; wait" | grep -c '^200$')
ok "$([ "$n" = 30 ]; echo $?)" "30 concurrent requests over the socket ($n ok)"
dk kill -s USR2 "$F" >/dev/null; sleep 3
ok "$([ "$(getn http://$N/index.php)" = 200 ]; echo $?)" "SIGUSR2 reload keeps serving (socket recreated by the new master)"
dk stop -t 20 "$F" >/dev/null
ok "$(c=$(exit_code "$F"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "php-fpm graceful stop (exit $(exit_code "$F"))"
ok "$(dk run --rm -v "$V":/v $NGX sh -c "test ! -S $SOCK"; echo $?)" "the socket is gone after the stop"
dk start "$F" >/dev/null
wait_for 60 sh -c "[ \"\$(dk run --rm --network $N $NGX curl -s -o /dev/null -w '%{http_code}' http://$N/index.php)\" = 200 ]"
ok $? "after a restart the socket is recreated and nginx reaches it again"
if [ -n "$COMPAT_ENGINE_ROOT" ] && [ -d "$COMPAT_ENGINE_ROOT/tmp/proot" ]; then
    c=$(find "$COMPAT_ENGINE_ROOT/tmp/proot" -name 'prootsock-c-*' | wc -l)
    ok "$([ "$c" = 0 ]; echo $?)" "no connect alias left in PROOT_TMP_DIR ($c)"
fi
logs_on_fail fpm
no_leftovers
