# Runtime fixture for F-PYTHON: one PASS/FAIL line per check.
import hashlib, json, os, signal, socket, subprocess, sys, tempfile, threading, time, urllib.request

def check(ok, what):
    print(("PASS " if ok else "FAIL ") + what, flush=True)

total = []
def work(n):
    total.append(sum(range(n)))
threads = [threading.Thread(target=work, args=(100000,)) for _ in range(8)]
[t.start() for t in threads]; [t.join() for t in threads]
check(len(total) == 8, "python: 8 threads")

out = subprocess.run(["sh", "-c", "echo sub; exit 3"], capture_output=True, text=True)
check(out.stdout == "sub\n" and out.returncode == 3, "python: subprocess output and exit code")

p = subprocess.Popen(["tr", "a-z", "A-Z"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
check(p.communicate(b"piped")[0] == b"PIPED", "python: pipes to a child")

got = []
signal.signal(signal.SIGUSR1, lambda s, f: got.append(s))
os.kill(os.getpid(), signal.SIGUSR1); time.sleep(0.1)
check(got == [signal.SIGUSR1], "python: signal handler")

with tempfile.TemporaryDirectory() as d:
    path = os.path.join(d, "f")
    with open(path, "wb") as f:
        f.write(os.urandom(1 << 20)); f.flush(); os.fsync(f.fileno())
    os.rename(path, path + ".2")
    check(os.path.getsize(path + ".2") == 1 << 20, "python: write, fsync, rename 1 MiB")

try:
    socket.getaddrinfo("dns.google", 443)
    check(True, "python: DNS lookup")
except OSError as e:
    check(False, f"python: DNS lookup ({e})")
try:
    with urllib.request.urlopen("https://dns.google/resolve?name=example.com", timeout=15) as r:
        check("Answer" in json.load(r), "python: HTTPS request (certificate verified)")
except Exception as e:
    check(False, f"python: HTTPS request ({e})")

# multiprocessing uses POSIX named semaphores (sem_open, /dev/shm).
try:
    import multiprocessing as mp
    with mp.Pool(2) as pool:
        check(pool.map(abs, [-1, -2, -3]) == [1, 2, 3], "python: multiprocessing.Pool")
except Exception as e:
    check(False, f"python: multiprocessing.Pool ({type(e).__name__}: {e})")
try:
    import multiprocessing as mp
    lock = mp.Lock(); lock.acquire(); lock.release()
    check(True, "python: multiprocessing.Lock")
except Exception as e:
    check(False, f"python: multiprocessing.Lock ({type(e).__name__}: {e})")
