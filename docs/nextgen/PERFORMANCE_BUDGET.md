# Performance budget and gates

Budgets are provisional and anchored to measured v0.1.1 values
(`PERFORMANCE_BASELINE.md`). A breach triggers investigation; a change
that keeps breaching after investigation is fixed, reverted, or explicitly
accepted by the owner.

| ID | Budget | Gate value (host) | Current (host) | Device |
|---|---|---|---|---|
| B-01 | Steady-state idle CPU of the whole tree (daemon + every container) | 0 ticks per 10 s window, every window after the first 30 s | 0 | PENDING |
| B-02 | Steady-state idle wakeups of the whole tree | 0 per 10 s window after the first 30 s (a single post-activity window of runtime housekeeping is allowed) | 0 | PENDING |
| B-03 | Daemon RSS, idle | ≤ v0.1.1 + 10% (12.9 MiB empty, 19.6 MiB with four containers) | 12.2 / 18.7 MiB | PENDING |
| B-04 | Per-container overhead | ≤ 4 MiB per idle container (PRoot + workload) | ≈ 3.3 MiB | PENDING |
| B-05 | Latency (`run --rm true`, `exec true`, start) | median within +15% of v0.1.1 in an interleaved A/B | within noise | PENDING |
| B-06 | TCP throughput (forwarder, container to container) | median within −15% of v0.1.1 | within noise | PENDING |
| B-07 | Features not in use | no extra process, goroutine or timer (Compose, Web Panel, networks) | met by design and the idle gates | PENDING |
| B-08 | Crash loop | ≤ 1 start per minute after the backoff ramp | met (`restart-policies.sh`) | PENDING |
| B-09 | Binary size | stripped arm64 engine ≤ 9.5 MB | 8.72 MB | — |
| B-10 | Battery / thermal | owner-assisted only: repeated, controlled trials; an isolated battery percentage is not evidence | — | PENDING (owner) |

## How to run a gate

```sh
# interleave base and candidate on a quiet machine; never run builds meanwhile
for i in 1 2; do for b in base cand; do
  tests/perf/baseline.sh ./thothdock-$b ./proot results/$b-run$i.txt
done; done
tests/perf/compare.py 'results/base-run*.txt' 'results/cand-run*.txt'
# network mode (needs Garden PRoot with --net-ip)
PERF_NETWORK=1 tests/perf/baseline.sh ./thothdock-cand ./garden-proot results/net-run1.txt
```

A 10% difference in one run is not a regression; the gate compares
medians of interleaved repeats. The fsync-bound filesystem figure varies
several-fold on virtual disks and is reported, not gated.
