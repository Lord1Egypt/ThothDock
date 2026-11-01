# P-LMDB: LMDB (one big mmap'd file, reader lock table, msync/fsync) through
# the official tools from Alpine's packages: load, dump, stat and a
# concurrent reader/writer on a named volume, then a reopen.
# timeout: 240
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
V=$(name db); dk volume create "$V" >/dev/null
dk run --rm --network "$N" -v "$V":/v $IMG sh -c '
set -e
apk add --no-cache lmdb-tools >/dev/null
mkdir -p /v/env
i=0; : > /tmp/in; echo VERSION=3 > /tmp/in; echo format=print >> /tmp/in; echo type=btree >> /tmp/in; echo HEADER=END >> /tmp/in
while [ $i -lt 5000 ]; do printf " k%05d\n v%05d\n" $i $i >> /tmp/in; i=$((i+1)); done; echo DATA=END >> /tmp/in
mdb_load -f /tmp/in /v/env
echo "entries $(mdb_stat /v/env | sed -n "s/.*Entries: //p")"
mdb_dump /v/env | grep -c "^ [0-9a-f]"
(for r in 1 2 3 4; do mdb_dump /v/env | grep -c "^ [0-9a-f]" & done; wait) | sort -u
# a second load while readers run, then the reader count must stay consistent
(mdb_dump /v/env | grep -c "^ [0-9a-f]" &) ; mdb_load -f /tmp/in /v/env; wait
mdb_stat -e /v/env | grep -E "Number of pages used|Last transaction" ' > "$TMPD/lmdb.out" 2>&1
cat "$TMPD/lmdb.out"
ok "$(grep -q '^entries 5000$' "$TMPD/lmdb.out"; echo $?)" "5000 entries loaded and counted by mdb_stat"
ok "$([ "$(grep -c '^10000$' "$TMPD/lmdb.out")" -ge 2 ]; echo $?)" "mdb_dump reads all 5000 entries (10000 hex lines), also from 4 concurrent readers"
dk run --rm -v "$V":/v $IMG sh -c 'apk add --no-cache lmdb-tools >/dev/null && mdb_dump /v/env | grep -c "^ [0-9a-f]"' > "$TMPD/lmdb2.out" 2>&1
ok "$([ "$(tail -1 "$TMPD/lmdb2.out")" = 10000 ]; echo $?)" "a new container reopens the environment with all keys"
no_leftovers
