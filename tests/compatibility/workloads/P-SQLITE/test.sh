# P-SQLITE: SQLite (WAL, mmap'd shared memory file, POSIX/OFD locks, fsync)
# with concurrent processes, a SIGKILLed transaction, checkpointing and
# reopening, on a named volume.
# timeout: 240
IMG=python:3.13-alpine
dk pull -q $IMG >/dev/null
setup
V=$(name db); dk volume create "$V" >/dev/null
dk run -i --rm -v "$V":/v $IMG python - /v/t.db < fixture.py > "$TMPD/sqlite.out" 2>&1
cat "$TMPD/sqlite.out"
dk run --rm -v "$V":/v $IMG python -c "import sqlite3; c = sqlite3.connect('/v/t.db'); print(c.execute('select count(*) from t').fetchone()[0])" > "$TMPD/after.out" 2>&1
ok "$([ "$(cat "$TMPD/after.out")" = 902 ]; echo $?)" "a new container sees all 902 rows"
no_leftovers
