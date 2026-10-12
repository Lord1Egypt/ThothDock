# E-RABBITMQ: RabbitMQ 4 (Erlang/BEAM, epoll, many file descriptors):
# authentication, durable queue, publish/get through the management HTTP API
# from another container, persistent message across restart.
# timeout: 480
IMG=rabbitmq:4-management-alpine
dk pull -q $IMG >/dev/null; dk pull -q nginx:1.28-alpine >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
run_bg mq --hostname compatmq -e RABBITMQ_DEFAULT_USER=compat -e RABBITMQ_DEFAULT_PASS=disposable-test-only -v "$V":/var/lib/rabbitmq $IMG
M=$(name mq)
api() { dk run --rm --network "$N" nginx:1.28-alpine curl -s --max-time 20 -u compat:disposable-test-only -H 'content-type: application/json' "$@"; }
ready() { wait_for 240 sh -c "dk exec $M rabbitmq-diagnostics -q check_running && dk run --rm --network $N nginx:1.28-alpine curl -sf -u compat:disposable-test-only http://$M:15672/api/overview"; }
ready; ok $? "broker running, management API reachable from another container"
[ "$fails" = 0 ] || { logs_on_fail mq; return 0; }
ok "$([ "$(dk run --rm --network "$N" nginx:1.28-alpine curl -s -o /dev/null -w '%{http_code}' -u compat:wrong http://$M:15672/api/overview)" = 401 ]; echo $?)" "wrong password refused (401)"
api -X PUT http://$M:15672/api/queues/%2F/durable_q -d '{"durable": true}' >/dev/null
api -X PUT http://$M:15672/api/queues/%2F/q2 -d '{"durable": false}' >/dev/null
api -X PUT http://$M:15672/api/exchanges/%2F/fan -d '{"type": "fanout", "durable": true}' >/dev/null
api -X POST http://$M:15672/api/bindings/%2F/e/fan/q/durable_q -d '{}' >/dev/null
api -X POST http://$M:15672/api/bindings/%2F/e/fan/q/q2 -d '{}' >/dev/null
i=0; while [ $i -lt 10 ]; do api -X POST http://$M:15672/api/exchanges/%2F/fan/publish -d "{\"properties\": {\"delivery_mode\": 2}, \"routing_key\": \"\", \"payload\": \"m$i\", \"payload_encoding\": \"string\"}" >/dev/null & i=$((i + 1)); done; wait
got=$(api -X POST http://$M:15672/api/queues/%2F/q2/get -d '{"count": 20, "ackmode": "ack_requeue_false", "encoding": "auto"}' | grep -o '"payload":"m[0-9]"' | wc -l)
ok "$([ "$got" = 10 ]; echo $?)" "fanout: 10 concurrent publishes reach a second subscriber queue ($got)"
dk stop -t 60 "$M" >/dev/null
ok "$(c=$(exit_code "$M"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "docker stop ends the broker (exit $(exit_code "$M"))"
dk start "$M" >/dev/null; ready
got=$(api -X POST http://$M:15672/api/queues/%2F/durable_q/get -d '{"count": 20, "ackmode": "ack_requeue_false", "encoding": "auto"}' | grep -o '"payload":"m[0-9]"' | wc -l)
ok "$([ "$got" = 10 ]; echo $?)" "durable queue keeps 10 persistent messages across restart ($got)"
logs_on_fail mq
no_leftovers
