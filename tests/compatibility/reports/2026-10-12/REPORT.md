# ThothDock 0.2.1 / ThothTerm 0.4.1 compatibility round, 2026-10-12

Status: **source and host evidence complete; Android validation of the final build, signing and release are NOT done** (see "Gates").

## What the evidence covers

- Host (Linux x86-64, PRoot built from the pinned termux/proot + Garden patches 0001–0017, engine ThothDock fix branch): 38 workloads, `summary.md`/`matrix.csv`. 34 PASS, 2 EXPECTED_UNSUPPORTED (Docker in Docker, socket mounting), 1 BLOCKED (MinIO: image not pullable), 1 PARTIAL (SeaweedFS stop), plus 5 deferred P1 rows (NOT_TESTED).
- Garden host checks: `guest-semantics-check.sh` (24 checks), `sysvipc-check.sh` 5, `net-ip-check.sh` 18, `fork-recovery-check.sh` 24, `owner-check.sh` 2: all PASS.
- ThothDock: `go test -race` with a real PRoot, Docker CLI smoke, lifecycle, restart policies, networks, Compose, upgrade-from-v0.1.1: all PASS.
- Android (SM-A165F, Android 16, kernel 6.12, no CONFIG_SYSVIPC, 39-bit user VA, isolated QA package, production untouched): see "Android device gate" below. The earlier-build run (IPC probe 42/42 ×10, PostgreSQL 19/19) is superseded by the final-build run.

## Root causes (one per mechanism)

| Failure | Mechanism | Fix | Where |
|---|---|---|---|
| PostgreSQL `shmget` ENOSYS | Android kernel lacks System V IPC; ThothDock never enabled PRoot's `--sysvipc` | pass `--sysvipc` with a per-PRoot namespace; segments kept in-process as memfds (no helper process, no `/dev/ashmem`), concurrent-`shmat` and `semtimedop` bugs fixed | patch 0010, `internal/runtime/proot.go` |
| MongoDB 8 `MmapAligned()` | TCMalloc (bundled) assumes 48-bit user addresses and places tagged 1 GiB regions above bit 39; the phone kernel has `CONFIG_ARM64_VA_BITS_39`: a fixed 1 GiB mmap at `0xf0f00000000` fails with ENOMEM (probe on the phone; reproduced with qemu `-R 0x8000000000`; MongoDB 7 runs). It is a virtual-address limit, not RAM, and not PRoot (PRoot passes mmap through) | **none possible in PRoot/ThothDock**; unsupported on 39-bit kernels; MongoDB 7 or an allocator built for 39 bits | owner decision |
| Redis 7.4+/8, Valkey exit 127 | entrypoint `setpriv --reuid --regid --clear-groups`, `--inh-caps/--ambient-caps/--bounding-set`: kernel refuses `setgroups`/`capset`/cap `prctl` to the unprivileged app; fake_id0 forgave only `euid==0` | same errno-only forgiveness for the already-modelled kept capability | patch 0011 |
| nginx `docker stop` killed (137) | PRoot treated SIGQUIT as its own crash and killed all tracees | pass SIGQUIT to the guest | patch 0012 |
| RabbitMQ epmd | `uname` node name was the device's, `/etc/hosts` named the container | `--hostname` (only `uname`'s node name) | patch 0013 + runtime |
| `docker exec … psql` (Unix socket) | host path of a container socket exceeds `sun_path`; PRoot bound it at a random name known to one PRoot only | socket lives at its real path behind random short aliases; peer names translated back | patch 0014 |
| DNS on user networks with a resolver stub host | `--net-ip` redirected 127.0.0.53 to the container itself | leave 127.0.0.53/54 alone | patch 0015 |
| sshd privilege separation aborts the container | guest `chroot` (Termux emulation) hit a talloc type abort, then an uninitialised path comparison | bindings destructor uses its own namespace; guest path initialised | patches 0016, 0017 |
| php-fpm `docker stop` ends in SIGKILL | php-fpm `setsid`s out of the group ThothDock signals | also signal the container's main process when outside the group | `proot.go` |
| (diagnostics) "anonymous volumes" warning | warned although a named volume covered the path | warn only for uncovered paths | `create.go` |

## Linux facilities: supported / limited / unsupported

Supported and tested: fork/vfork/clone/exec, signals (incl. process-group, SIGQUIT, setsid daemons), pipes, futex and process-shared mutex/semaphore, flock/fcntl/OFD locks, mmap incl. shared and file mappings, 8 GiB reservations, System V shm/sem/msg, fsync, rename, hard and symbolic links, O_TMPFILE, SQLite WAL, LMDB, TCP/UDP/UNIX sockets (also on paths over 108 bytes, across processes and containers), epoll/poll, inotify (Fluent Bit), PTYs and ssh, `chroot` as fake root, TLS and HTTP/2, DNS.

Limited: POSIX shared memory and named semaphores need `/dev/shm` (absent on Android: Python `multiprocessing`, `shm_open` fail; PostgreSQL falls back to System V); `/proc` is the device's; no PID namespace, so process matching must use command lines; `chroot` with bind mounts below the new root unsupported; user address space is the kernel's (39 bits on this phone).

Unsupported by design: Docker in Docker (`dockerd` needs namespaces/cgroups/mounts), mounting the engine socket on Android, UDP host publishing, cgroup limits, `--privileged`.

## Test notes you should know

- P-UNIX fails 4 checks on PRoot 0001–0013, D-PHPFPM's stop and the signal unit test fail without the engine change, guest chroot check and S-OPENSSH fail without 0016/0017: the regression tests are real negative controls.
- H-SEAWEEDFS (bounded investigation, owner decision 3): **application-specific graceful-shutdown hang, data intact, PARTIAL**. After SIGTERM, SeaweedFS 4.48 holding stored objects did not finish within 60, 150 or 300 s (`docker stop -t N`), and ThothDock killed it at the deadline (exit 137, as Docker does); `docker kill -s TERM` shows the same (still running after 130 s); with an empty store it stops in 22 s. A SIGQUIT goroutine dump (delivered by patch 0012) shows the main goroutine parked in `select {}` and the shutdown hook blocked in `Filer.Shutdown` → `LogBuffer.WaitForShutdown`, a Go `sync.WaitGroup` inside the application: no OS call, signal or PRoot operation is involved. The object's SHA-256 was identical after restart in every run. Not a ThothDock lifecycle defect; not a release blocker. (Timings here come from `date` on a WSL host whose clock drifts, which is why some durations looked shorter than the grace period.)
- C-COREDNS was re-run once after relabelling a note (its result row came from that rerun, 9/9).
- Docker Hub's anonymous quota (100 pulls/hour) interrupted two runs; `runner/prepull.sh` pulls through `mirror.gcr.io` (same digests, checked against `matrix.json`).
- Several test-harness bugs were found and fixed on the way (variable clobbering, content types, hex dump format, `/proc` shared with the host, key permissions); none was counted as a runtime result.

## Resource comparison (host only)

Interleaved baseline (v0.2.0 + patches 0001–0009) vs candidate, two rounds, same root and images, four lightweight containers (Alpine sleep, nginx, memcached, BusyBox httpd), 60 s idle:

| | baseline | candidate |
|---|---|---|
| processes / threads (whole tree) | 21 / 42 | 21 / 41 |
| RSS (tree) | 72.4–72.6 MB | 71.9–72.3 MB |
| engine RSS / threads / fds | 15.0–15.1 MB / 13 / 24 | 14.9–15.0 MB / 12 / 24 |
| CPU in 60 s | 10–20 ms | 0–10 ms |
| context switches in 60 s | 80–81 | 69–71 |
| `docker run --rm alpine true` | 298 / 307 ms | 297 / 308 ms |
| leftover `prootshm*` / `prootsock-c*` | 0 / 0 | 0 / 0 |

Earlier on the same host: `--sysvipc` adds no measurable cost to a process-heavy workload (ABBA, 16 runs each: median 15.56 s vs 15.36 s) and idle PostgreSQL showed equal CPU and wake-ups; pgbench 381–472 tps (baseline, kernel IPC) vs 467–691 tps (PRoot IPC), noisy. Stop latency: one `docker stop` costs one `/proc/<pid>/task/<pid>/children` read more. **No new thread, helper process, timer or polling loop exists in any patch.** Android power and phantom-process budgets are **unmeasured** (production workloads were never touched; the earlier cap incident is documented).

## Gates

| Gate | State |
|---|---|
| Host correctness, regression, security review | met (docs/PRELEASE_SECURITY_REVIEW_0.2.1.md) |
| Android validation of the final build (all patches, workloads, MongoDB 8/7 behaviour on the 39-bit kernel, resource/process budget) | **not met**: phone not reachable |
| Signing with the production key (cert `ca326f06…`) | **not done**: the keystore is the owner's; the project's rule is that the owner runs `~/secure/create-thothterm-key.sh sign trixie` |
| Reproducible F-Droid build, F-Droid MR update (!50342, open at the 0.4.0 recipe, pipeline green) | not started: needs frozen release commits |
| Versions/tags | none created; suggested ThothDock `v0.2.1`, `trixie-v0.4.1` (versionCode 401); the app's engine pin is a fix-branch commit and must be re-pinned to the release commit first |

## Owner decisions (2026-10-12)

1. MongoDB 8: documented as incompatible with 39-bit user address spaces; MongoDB 7.0 (v7.0.43 verified on the host) is the recommended alternative; no workaround attempted (docs/COMPATIBILITY.md).
2. SeaweedFS: investigated, application-specific, accepted as PARTIAL (see above).
3. Ship 0.4.1 without a device re-run of all workloads (not recommended), or reconnect the phone for one focused QA round with the QA package.

## Android device gate (final build, patches 0010–0018)

Device: SM-A165F, Android 16, isolated package `com.thothterm.debian.qa.sysvipc`, QA APK `qa4.apk` sha256 `433d8f290201634c4d3b2e7b3b7b556eb527a69dee14e3fd44efe7d6acdf228c` (engine pin 81d8f0d + patches 0010–0018). Baseline APK `qa-base.apk` sha256 `24963d90…` (trixie-v0.4.0). Raw logs: `device-final-evidence.tar`, `device-ab-measurements.txt`.

Result: every mandatory workload PASS on the candidate (P-CORE, P-IPC, P-UNIX, PostgreSQL, MongoDB 7, Redis, Valkey, Nginx, RabbitMQ, PHP-FPM, DNS, OpenSSH) except MongoDB 8, which fails as documented (39-bit address space).

- Device-only defect found and fixed: Redis/Valkey privilege drop failed because Android apps carry `SECBIT_KEEP_CAPS_LOCKED` (kernel refuses `PR_SET_KEEPCAPS`). Patch 0018 reports success for fake root; host repro plus negative control; PASS on the phone.
- G-BUSYBOX: the earlier failure was a harness race (USR1 sent before the trap was installed), not networking; fixed in the workload.
- Probe staging: `/data/local/tmp` is not executable for the app; probes are copied into the app files directory (verified).
- RabbitMQ: three early device runs failed or were partial. Cause was the harness readiness check, which ran `rabbitmq-diagnostics` through `docker exec` repeatedly; each call starts another Erlang VM and starves the booting broker. Readiness now watches the log, then the management API. After the change: two consecutive PASS (6/6, 127 s and 115 s). Not a regression: the baseline cannot run RabbitMQ at all (no `--hostname`).
- Open, unexplained: a guest SIGQUIT was ignored on the device in some earlier attempts; not reproduced in the final run. Not a release blocker (SIGKILL after the stop timeout still ends containers).

Baseline vs candidate, same device, interleaved, 60 s idle with four containers (two rounds each): processes 17/17; threads 39–40 vs 40–41; tree RSS ≈187–192 MB both; CPU 30–50 ms vs 20–40 ms; context switches 65–70 vs 69; fds 245/245; engine RSS 12–13 MB both; stop latency within noise; no leftover processes, shm or socket aliases. No regression observed. Battery was **not measured** (no controlled measurement was performed).

Not done: production app is not installed on the phone and no verified backup of its data exists; nothing was reinstalled.
