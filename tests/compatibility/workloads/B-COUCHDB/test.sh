# B-COUCHDB: Apache CouchDB 3.5 (Erlang/BEAM schedulers, epoll, fsync):
# auth, databases, documents, Mango index and query, update/delete with
# revisions, local replication, restart persistence. Client: curl from the
# image itself.
# timeout: 420
IMG=couchdb:3.5
dk pull -q $IMG >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
run_bg db -e COUCHDB_USER=admin -e COUCHDB_PASSWORD=disposable-test-only -v "$V":/opt/couchdb/data $IMG
D=$(name db)
cu() { dk exec "$D" curl -s --max-time 20 -u admin:disposable-test-only -H 'Content-Type: application/json' "$@"; }
wait_for 180 sh -c "dk exec $D curl -sf -u admin:disposable-test-only http://127.0.0.1:5984/_up"
ok $? "CouchDB is up"
[ "$fails" = 0 ] || { logs_on_fail db; return 0; }
for db in _users _replicator compat compat_copy; do cu -X PUT "http://127.0.0.1:5984/$db" >/dev/null; done
ok "$([ "$(dk exec "$D" curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:5984/compat)" = 401 ]; echo $?)" "unauthenticated request refused (401)"
i=0; while [ $i -lt 20 ]; do cu -X PUT "http://127.0.0.1:5984/compat/d$i" -d "{\"n\": $i, \"site\": \"s$((i % 3))\"}" >/dev/null; i=$((i + 1)); done
ok "$(cu http://127.0.0.1:5984/compat | grep -q '"doc_count":20'; echo $?)" "20 documents created"
cu -X POST http://127.0.0.1:5984/compat/_index -d '{"index": {"fields": ["site"]}, "name": "by-site"}' >/dev/null
q=$(cu -X POST http://127.0.0.1:5984/compat/_find -d '{"selector": {"site": "s1"}}' | grep -o '"_id"' | wc -l)
ok "$([ "$q" = 7 ]; echo $?)" "Mango index and query ($q docs)"
rev=$(cu http://127.0.0.1:5984/compat/d0 | sed -n 's/.*"_rev":"\([^"]*\)".*/\1/p')
cu -X PUT http://127.0.0.1:5984/compat/d0 -d "{\"_rev\": \"$rev\", \"n\": 100}" | grep -q '"ok":true'; ok $? "update with the current revision"
cu -X PUT http://127.0.0.1:5984/compat/d0 -d "{\"_rev\": \"$rev\", \"n\": 101}" | grep -q conflict; ok $? "stale revision is a conflict"
rev=$(cu http://127.0.0.1:5984/compat/d1 | sed -n 's/.*"_rev":"\([^"]*\)".*/\1/p')
cu -X DELETE "http://127.0.0.1:5984/compat/d1?rev=$rev" | grep -q '"ok":true'; ok $? "delete"
cu -X POST http://127.0.0.1:5984/_replicate -d '{"source": "http://admin:disposable-test-only@127.0.0.1:5984/compat", "target": "http://admin:disposable-test-only@127.0.0.1:5984/compat_copy"}' | grep -q '"ok":true'
ok $? "one-shot local replication"
ok "$(cu http://127.0.0.1:5984/compat_copy | grep -q '"doc_count":20'; echo $?)" "replica has the documents (19 + design doc)"
dk stop -t 30 "$D" >/dev/null
ok "$(c=$(exit_code "$D"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "docker stop ends CouchDB (exit $(exit_code "$D"))"
dk start "$D" >/dev/null
wait_for 180 sh -c "dk exec $D curl -sf -u admin:disposable-test-only http://127.0.0.1:5984/compat | grep -q '\"doc_count\":20'"
ok $? "restart keeps the documents"
logs_on_fail db
no_leftovers
