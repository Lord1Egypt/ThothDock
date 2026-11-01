# M-MAILPIT: Mailpit SMTP test server (Go, SQLite): submit synthetic mail
# over SMTP from another container, read it through the HTTP API, restart
# with the on-volume database. Nothing leaves the test network.
# timeout: 240
IMG=axllent/mailpit:v1.27; CLI=alpine:3.22
dk pull -q $IMG >/dev/null; dk pull -q $CLI >/dev/null
setup
V=$(name db); dk volume create "$V" >/dev/null
run_bg mp -v "$V":/data $IMG --database /data/mailpit.db --smtp 0.0.0.0:1025 --listen 0.0.0.0:8025
M=$(name mp)
api() { dk run --rm --network "$N" $CLI wget -q -O - -T 10 "http://$M:8025$1"; }
smtp() { dk run -i --rm --network "$N" $CLI nc -w 5 $M 1025; }
wait_for 60 sh -c "dk run --rm --network $N $CLI wget -q -O /dev/null -T 3 http://$M:8025/api/v1/info"
ok $? "HTTP API answers"
i=1; while [ $i -le 5 ]; do
    printf "EHLO compat\r\nMAIL FROM:<a@test.invalid>\r\nRCPT TO:<b@test.invalid>\r\nDATA\r\nSubject: compat $i\r\nFrom: a@test.invalid\r\nTo: b@test.invalid\r\n\r\nbody $i\r\n.\r\nQUIT\r\n" | smtp > "$TMPD/smtp$i.out" 2>&1
    i=$((i+1))
done
ok "$(grep -q '^250' "$TMPD/smtp5.out"; echo $?)" "SMTP submission accepted (last reply: $(grep -m1 '^250.*' "$TMPD/smtp5.out" | tail -1 | tr -d '\r'))"
ok "$(api /api/v1/messages | grep -q '"total":5'; echo $?)" "the API lists the 5 messages"
ok "$(api /api/v1/search?query=subject:%22compat%203%22 | grep -q 'compat 3'; echo $?)" "search finds one by subject"
for i in 1 2 3; do printf "EHLO c\r\nMAIL FROM:<a@test.invalid>\r\nRCPT TO:<b@test.invalid>\r\nDATA\r\nSubject: par\r\n\r\nx\r\n.\r\nQUIT\r\n" | smtp >/dev/null 2>&1 & done; wait
ok "$(api /api/v1/messages | grep -q '"total":8'; echo $?)" "3 concurrent SMTP sessions added 3 messages"
dk stop -t 20 "$M" >/dev/null
ok "$(c=$(exit_code "$M"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "stop (exit $(exit_code "$M"))"
dk start "$M" >/dev/null
wait_for 60 sh -c "dk run --rm --network $N $CLI wget -q -O - -T 3 http://$M:8025/api/v1/messages | grep -q '\"total\":8'"
ok $? "all 8 messages are still there after a restart"
logs_on_fail mp
no_leftovers
