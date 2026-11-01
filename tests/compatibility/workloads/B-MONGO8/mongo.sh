# mongo_db IMAGE: MongoDB (WiredTiger, many threads, TCMalloc).
mongo_db() {
    IMG=$1
    dk pull -q "$IMG" >/dev/null || { ok 1 "pull $IMG"; return; }
    setup
    V=$(name data); dk volume create "$V" >/dev/null
    run_bg db -v "$V":/data/db "$IMG" mongod --bind_ip_all --wiredTigerCacheSizeGB 0.25
    D=$(name db)
    js() { dk exec "$D" mongosh --quiet --norc "mongodb://127.0.0.1:27017/compat" --eval "$1" 2>&1; }
    ready() { wait_for 120 sh -c "dk exec $D mongosh --quiet --norc --eval 'db.runCommand({ping:1}).ok' 2>/dev/null | grep -q '^1\$'"; }
    ready; ok $? "mongod starts and answers ping"
    if [ "$fails" != 0 ]; then
        dk logs --tail 6 "$D" 2>&1 | sed 's/^/    | /'
        dk logs "$D" 2>&1 | grep -q 'MmapAligned() failed' && \
            note "TCMalloc address placement failed: this kernel's user address space is smaller than the 48 bits MongoDB 8's TCMalloc assumes"
        return 0
    fi
    note "version $(js 'db.version()')"
    js 'db.obs.insertMany(Array.from({length: 5000}, (_, i) => ({i, site: "s" + (i % 7)}))).acknowledged' >/dev/null
    ok "$([ "$(js 'db.obs.countDocuments({})')" = 5000 ]; echo $?)" "insert 5000 documents"
    ok "$([ "$(js 'db.obs.countDocuments({site: "s3"})')" = 714 ]; echo $?)" "query"
    ok "$([ "$(js 'db.obs.updateMany({site: "s3"}, {$set: {f: 1}}).modifiedCount')" = 714 ] && [ "$(js 'db.obs.deleteMany({i: {$lt: 100}}).deletedCount')" = 100 ]; echo $?)" "update and delete"
    js 'db.obs.createIndex({i: 1})' >/dev/null
    ok "$([ "$(js 'db.obs.find({i: 4242}).explain().queryPlanner.winningPlan.inputStage.stage')" = IXSCAN ]; echo $?)" "index scan"
    ok "$([ "$(js 'db.obs.aggregate([{$group: {_id: "$site", n: {$sum: 1}}}]).toArray().length')" = 7 ]; echo $?)" "aggregation"
    for k in 1 2 3 4; do js "db.par.insertMany(Array.from({length: 250}, (_, i) => ({k: $k, i})))" >/dev/null & done; wait
    ok "$([ "$(js 'db.par.countDocuments({})')" = 1000 ]; echo $?)" "4 concurrent clients"
    dk stop -t 30 "$D" >/dev/null
    ok "$([ "$(exit_code "$D")" = 0 ]; echo $?)" "docker stop: clean exit 0"
    dk start "$D" >/dev/null; ready
    ok "$([ "$(js 'db.obs.countDocuments({}) + db.par.countDocuments({})')" = 5900 ]; echo $?)" "restart keeps the data"
    dk kill "$D" >/dev/null; dk start "$D" >/dev/null; ready
    ok "$([ "$(js 'db.obs.countDocuments({}) + db.par.countDocuments({})')" = 5900 ]; echo $?)" "journal recovery after SIGKILL keeps the data"
    logs_on_fail db
}
