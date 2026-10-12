# SQLite on a named volume: WAL, concurrent processes, checkpoints,
# kill -9 atomicity, persistence. PASS/FAIL lines.
import multiprocessing, os, signal, sqlite3, sys, time

DB = sys.argv[1] if len(sys.argv) > 1 else "/v/t.db"

def check(ok, what):
    print(("PASS " if ok else "FAIL ") + what, flush=True)

def connect():
    c = sqlite3.connect(DB, timeout=30, isolation_level=None)
    return c

def writer(n, start):
    c = connect()
    for i in range(start, start + n):
        c.execute("insert into t (v) values (?)", (i,))
    c.close()

def reader(stop, out):
    c = connect()
    bad = 0
    while not stop.is_set():
        a, b = c.execute("select count(*), coalesce(sum(1),0) from t").fetchone()
        bad += a != b
    out.put(bad)

if __name__ == "__main__":
    for ext in ("", "-wal", "-shm"):
        try: os.unlink(DB + ext)
        except FileNotFoundError: pass
    c = connect()
    check(c.execute("pragma journal_mode=wal").fetchone()[0] == "wal", "sqlite: WAL mode on the volume (mmap'd -shm)")
    c.execute("pragma synchronous=full")
    c.execute("create table t (id integer primary key, v integer)")
    stop = multiprocessing.Event(); out = multiprocessing.Queue()
    readers = [multiprocessing.Process(target=reader, args=(stop, out)) for _ in range(3)]
    writers = [multiprocessing.Process(target=writer, args=(300, k * 1000)) for k in range(3)]
    [p.start() for p in readers + writers]; [p.join(120) for p in writers]
    stop.set(); [p.join(30) for p in readers]
    check(all(p.exitcode == 0 for p in writers + readers), "sqlite: 3 writer and 3 reader processes finished")
    check(c.execute("select count(*) from t").fetchone()[0] == 900, "sqlite: 900 rows from 3 concurrent writers")
    check(c.execute("pragma integrity_check").fetchone()[0] == "ok", "sqlite: integrity_check ok")
    r = c.execute("pragma wal_checkpoint(truncate)").fetchone()
    check(r[0] == 0, f"sqlite: wal_checkpoint(truncate) busy={r[0]}")
    # Atomicity: a child dies in the middle of a big transaction.
    pid = os.fork()
    if pid == 0:
        k = connect(); k.execute("begin")
        for i in range(100000): k.execute("insert into t (v) values (?)", (-i,))
        os.kill(os.getpid(), signal.SIGKILL)
    os.waitpid(pid, 0)
    check(c.execute("select count(*) from t").fetchone()[0] == 900 and c.execute("select count(*) from t where v < 0").fetchone()[0] == 0,
          "sqlite: a SIGKILLed uncommitted transaction leaves nothing")
    c.execute("insert into t (v) values (7777)")
    c.close()
    # Persistence: reopen in a fresh process.
    c = connect()
    check(c.execute("select count(*) from t").fetchone()[0] == 901, "sqlite: reopened database has all committed rows")
    # The WAL is recovered after an unclean end: kill a process with committed WAL pages.
    pid = os.fork()
    if pid == 0:
        k = connect(); k.execute("insert into t (v) values (8888)"); os.kill(os.getpid(), signal.SIGKILL)
    os.waitpid(pid, 0)
    check(connect().execute("select count(*) from t where v = 8888").fetchone()[0] == 1, "sqlite: a committed row survives SIGKILL (WAL recovery)")
