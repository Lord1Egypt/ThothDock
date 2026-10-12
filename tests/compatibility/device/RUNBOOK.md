# Android validation runbook (final device gate, ThothDock 0.2.1 / ThothTerm 0.4.1)

Rules: only the isolated QA package `com.thothterm.debian.qa.sysvipc` (debug-signed, own data). Never install over, uninstall or stop the production package `com.thothterm.debian`, its containers, images or volumes. Do not test while production workloads are running if the process count (below) is above 20.

## 1. Reconnect

1. On the phone: Developer options, Wireless debugging on, note `IP:PORT` (the port changes every session) or pair with the code.
2. From the host (WSL): `ADB=<Windows adb.exe>`; `$ADB connect IP:PORT`; `$ADB devices -l` must list the SM-A165F as `device`.
3. Keep the screen on and the phone charging during the run (`svc power stayon usb` is a device setting: do not change it without asking).

## 2. Protect production (before and after every step)

```
adb shell 'pm list packages | grep thothterm.debian'          # note whether production is installed
adb shell 'ps -A -o ARGS | grep -c "[r]ootfs=/data/user/0/com.thothterm.debian/files/thothdock/containers"'
adb shell 'logcat -d -b events | grep am_kill | tail -3'        # "Trimming phantom processes" = trouble
```

Android kills the oldest app children when the device-wide phantom-process count passes 32 (`dumpsys activity settings | grep max_phantom`). If production is installed with containers, run at most one QA workload at a time and stop it at once. Abort and report if `am_kill` names `libthothdock.so` of production.

## 3. Build and install the final QA APK

From `~/AndroidThothTerm/wt-sysvipc` (branch `fix/sysvipc-0.4.1`), after the engine commit is final:
`ANDROID_HOME=~/Android/Sdk JAVA_HOME=~/jdk21 PATH=<go1.26.8 toolchain>:$PATH ./gradlew --offline -PthothtermQaApplicationIdSuffix=.qa.sysvipc :garden-debian:assembleFdroidDebug`
Check the APK: package `com.thothterm.debian.qa.sysvipc`; `unzip -p APK assets/thothdock/components.properties` shows the exact engine commit; `libproot.so` contains `--hostname` and `prootsock-`. `adb install -r` (same debug signer keeps QA data).

## 4. Push, start, run

```
adb push tests/compatibility/{runner,workloads,matrix.json,probes/bin(arm64)} /data/local/tmp/thoth-sysvipc/compat/
adb push tests/compatibility/device/*  /data/local/tmp/thoth-sysvipc/qa/ ; add qa/resolv.conf (nameserver 1.1.1.1 / 8.8.8.8)
adb shell dumpsys deviceidle whitelist +com.thothterm.debian.qa.sysvipc      # run-as has no foreground: else no network
adb shell run-as <QA> sh /data/local/tmp/thoth-sysvipc/qa/start-engine.sh
adb shell run-as <QA> sh /data/local/tmp/thoth-sysvipc/qa/prepull.sh IMAGE...  # via mirror.gcr.io, avoids the Hub quota
adb shell run-as <QA> sh /data/local/tmp/thoth-sysvipc/qa/run-tests.sh LABEL ID...
```

arm64 probes: static musl builds (`probes/build.sh arm64` with an aarch64 musl compiler, or build inside arm64 Alpine under qemu-user as done in this round). NDK static binaries do not run on this phone (TLS alignment).

## 5. What to run (in this order; one at a time)

| Mechanism | Test IDs | Expected |
|---|---|---|
| System V IPC (0010) | P-IPC, A-POSTGRES | 45/45, 13/13; initdb picks `sysv`; no `prootshm*` left |
| Core semantics | P-CORE, G-BUSYBOX, G-ALPINE | all PASS (`core-probe` reports the address-space width: expect 38 or 39 bits) |
| Long Unix sockets (0014), setsid shutdown | P-UNIX, D-PHPFPM | 10/10, 11/11; no `prootsock-c*` left |
| Privilege drop, SIGQUIT, hostname (0011-0013) | C-REDIS, C-VALKEY, D-NGINX, E-RABBITMQ | 9/9, 9/9, 12/12, 6/6 |
| DNS (0015) | G-ALPINE (DNS, HTTPS) on a user network | PASS; the phone has no 127.0.0.53 stub |
| sshd/chroot (0016, 0017) | S-OPENSSH | 10/10 |
| MongoDB outcomes | B-MONGO8, B-MONGO7 | 8: fails with `MmapAligned()` (the documented 39-bit limit: record it, status UNSUPPORTED_BY_DESIGN); 7: expected PASS (first device run) |
| Lifecycle cleanup | `wrapup.sh` | 0 containers, volumes, engine processes; no leftover aliases |

Known open item from the first device attempt (2026-10-12, see `device-evidence`): G-BUSYBOX's signal test ran the container on a *user-defined network* and it was not running; the same container on the default network runs fine. Establish whether this is the `--net-ip` path on Android (127.77.0.0/16 addresses, `pidfd_getfd`, SELinux) or a harness issue before anything else: run `docker network create n; docker run -d --network n busybox:1.37 sleep 100; docker ps -a; docker logs`, check `daemon.log` and `logcat -b all | grep avc`.

## 6. Resource measurements (compare with the baseline)

Baseline: the 0.4.0 engine, built the same way, installed under the same QA id (`git -C ... worktree` at tag `trixie-v0.4.0`, `-PthothtermQaApplicationIdSuffix=.qa.sysvipc`), measured first; then the candidate. Earlier device figures for the 0.4.0 line are in PROJECT_MEMORY.md (engine about 6.7 MB, 15 threads, 0.05-0.08 % CPU, 0 wake-ups). Per variant, idle with four lightweight containers (alpine sleep, nginx, memcached, busybox httpd) for 60 s, from the app uid:

```
pids: engine pid and descendants (ps -A -o PID,PPID,ARGS)
per pid: /proc/PID/status  VmRSS, Threads, voluntary_ctxt_switches, nonvoluntary_ctxt_switches
         /proc/PID/stat    utime stime  (clock ticks, getconf CLK_TCK)
         ls /proc/PID/fd | wc -l
engine: also PostgreSQL idle (A-POSTGRES container up, no clients) 30 s
```

Report process count, thread count, RSS, CPU ms, context switches and fds before and after, plus residual `prootshm*`/`prootsock-*` files in `files/tdqa/root/tmp/proot`. No battery-equivalence claim without `dumpsys batterystats` over a long, comparable run; say "not measured" otherwise.

## 7. Finish

`wrapup.sh`, `dumpsys deviceidle whitelist -<QA>`, archive evidence with
`adb exec-out 'run-as <QA> tar cf - -C files/tdqa ev daemon.log' > qa-evidence.tar`, uninstall nothing. Record: build commit, APK sha256, `uname -r`, each row above as PASS/FAIL with the log.

## Gate

Release engineering (freeze, pin, reproducible build, unsigned artifacts) starts only when every row passes or is a documented expected outcome and no process-count or residual-resource regression is found.
