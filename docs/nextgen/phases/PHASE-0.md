# Phase 0 — Baseline, audit and performance protection

| Section | Content |
|---|---|
| Goals | A trustworthy picture of v0.1.1 and numbers to protect: idle CPU, wakeups, RSS, latencies |
| Out of scope | New features; device measurements while ADB is unavailable |
| Current state (before) | Strong docs and tests; no ownership map; no performance numbers |
| HLD | `../ARCHITECTURE.md` (as-is map); measurement from `/proc` only, no profiler |
| LLD | `tests/perf/baseline.sh`: utime+stime of the daemon and its process tree, context switches of **every thread** (Go runtime threads are separate tasks), RSS, `docker run --rm true`, exec, pull, 64 MiB fsync write/read, 64 MiB TCP via the forwarder and container to container; monotonic clock (WSL2 steps the wall clock) with its own overhead recorded |
| Sequence | start daemon → idle windows (empty) → pull → run/exec latency → four idle containers → fs → TCP → idle again |
| Dependencies | Docker CLI, a host PRoot |
| API changes | — |
| Data model | — |
| Android | Device run pending: read-only `/proc` sampling of the live production daemon is safe (no signal, no write); full harness only under the QA package |
| Security | — |
| Performance budget | defined in `../PERFORMANCE_BUDGET.md` |
| Test plan | three repeated runs; interleaved A/B for comparisons |
| Rollback | — |
| Definition of Done | host baseline (DONE), device baseline (PENDING) |
| Exit criteria | met on the host; existing tests all pass |
| Known limitations | WSL2 fsync timings vary up to 8×; only interleaved runs are comparable. CPU is in 10 ms clock ticks |
