# E-MOSQUITTO: Eclipse Mosquitto 2 (MQTT): password authentication, two
# subscribers on other containers, retained message persisted across a
# restart, clean stop.
# timeout: 240
IMG=eclipse-mosquitto:2
dk pull -q $IMG >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
run_bg mq -v "$V":/mosquitto/data $IMG sh -c '
    printf "listener 1883\nallow_anonymous false\npassword_file /mosquitto/config/pw\npersistence true\npersistence_location /mosquitto/data/\n" > /mosquitto/config/m.conf
    mosquitto_passwd -c -b /mosquitto/config/pw compat disposable-test-only && chown mosquitto /mosquitto/config/pw /mosquitto/data
    chmod 0700 /mosquitto/config/pw
    exec mosquitto -c /mosquitto/config/m.conf'
M=$(name mq)
pub() { dk run --rm --network "$N" $IMG mosquitto_pub -h "$M" -u compat -P disposable-test-only "$@"; }
wait_for 60 pub -t ping -m x; ok $? "authenticated publish"
dk run --rm --network "$N" $IMG mosquitto_pub -h "$M" -u compat -P wrong -t t -m x 2>&1 | grep -qi 'not authori\|refused'
ok $? "wrong password refused"
for s in 1 2; do
    dk run -d --restart no --network "$N" --name "$(name sub$s)" $IMG mosquitto_sub -h "$M" -u compat -P disposable-test-only -t compat/t -C 3 >/dev/null
done
sleep 3
for m in a b c; do pub -t compat/t -m "$m" -q 1; done
wait_for 30 sh -c "[ \"\$(dk inspect -f '{{.State.Running}}' $(name sub1) $(name sub2) | grep -c true)\" = 0 ]"
ok "$([ "$(dk logs "$(name sub1)" | tr '\n' ' ')" = "a b c " ] && [ "$(dk logs "$(name sub2)" | tr '\n' ' ')" = "a b c " ]; echo $?)" "two subscribers receive 3 messages in order"
pub -t compat/retained -m kept -r -q 1
dk stop -t 10 "$M" >/dev/null
ok "$([ "$(exit_code "$M")" = 0 ]; echo $?)" "docker stop: clean exit 0"
dk start "$M" >/dev/null
wait_for 60 pub -t ping -m x
r=$(dk run --rm --network "$N" $IMG mosquitto_sub -h "$M" -u compat -P disposable-test-only -t compat/retained -C 1 -W 10)
ok "$([ "$r" = kept ]; echo $?)" "retained message persisted across restart"
logs_on_fail mq
no_leftovers
