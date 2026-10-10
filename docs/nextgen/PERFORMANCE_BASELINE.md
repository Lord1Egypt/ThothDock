# Performance baseline

Measured with `tests/perf/baseline.sh` (all figures from `/proc`; CPU in
10 ms clock ticks; wakeups = voluntary + involuntary context switches of
every thread). Raw files: `evidence/phase0/`, `evidence/phase1-3/`,
`evidence/final/`.

**Host:** Intel i7-9750H (12 threads), 12.7 GiB, WSL2 kernel 6.18,
alpine:3.20, three 10 s idle windows per state.
**Device:** PENDING. ADB was unreachable in this round (the phone answered
ping; wireless debugging was off). The production containers were not
touched.

## v0.1.1 (three runs, 2026-10-10)

| Metric | Median | Range |
|---|---|---|
| Engine start to first answer | 206 ms | 184–209 |
| Idle, no containers: daemon CPU / wakeups per 10 s | 0 ms / 0 | 0 |
| Idle, no containers: daemon RSS | 11.7 MiB | 11.6–11.8 |
| Four idle containers: CPU / wakeups per 10 s, **whole tree** (daemon + 4 PRoot + 4 workloads) | 0 ms / 0 in steady state | one window after start-up: 34–63 wakeups (Go runtime returning memory), then 0 |
| Four idle containers: daemon RSS / tree RSS | 17.8 MiB / 30.9 MiB | ±0.2 MiB |
| Per idle container (PRoot + workload) | ≈ 3.3 MiB | |
| `docker run --rm alpine true` | 320 ms | 305–367 |
| `docker exec … true` | 198 ms | 190–250 |
| `docker pull alpine:3.20` (network) | 2.35 s | 2.32–2.46 |
| 64 MiB write with fsync / read, in a container | 292 ms / 213 ms | write varies 0.3–2.3 s on this virtual disk |
| 64 MiB TCP host → `-p` forwarder → container | 271 ms | 262–325 |
| 64 MiB TCP container → container (loopback) | 633 ms | 626–706 |

## Complete next-generation build vs v0.1.1 (interleaved, 2026-10-10)

Two v0.1.1 runs and two runs of the build with events, restart policies,
networks and the Web Panel compiled in, alternating, on the same PRoot;
then two runs with the four idle containers on a **user-defined network**
through Garden PRoot with `--net-ip`.

| Metric | v0.1.1 | next-gen | next-gen, user network |
|---|---|---|---|
| Idle CPU / wakeups, daemon and whole tree, empty | 0 / 0 | 0 / 0 | — |
| Idle CPU / wakeups, whole tree, four containers | 0 / 0 | 0 / 0 | **0 / 0** |
| Daemon RSS, empty | 11.8 MiB | 12.2 MiB (+4%) | — |
| Daemon RSS / tree RSS, four containers | 17.9 / 30.9 MiB | 18.7 / 31.7 MiB (+4% / +3%) | 18.7 / 32.1 MiB |
| Engine start | 199–505 ms (one slow run) | 191–203 ms | 218 ms |
| `run --rm true` / `exec true` | 342 / 199 ms | 313 / 195 ms | 328 / 203 ms |
| TCP via forwarder / container to container | 306 / 741 ms | 279 / 644 ms | 260 / 652 ms |

Reading: the idle profile that explains the golden phone observation is
unchanged, including with per-container addressing. RSS grew by about
0.5–0.8 MiB. Latency and throughput differences are within run-to-run
noise on this machine; none is claimed as an improvement.

Stripped arm64 engine binary: 7,864,480 B (v0.1.1) → 7,995,552 B (Phases
1–4) → 8,716,448 B (with the Web Panel).
