# Test strategy

A feature is DONE only when the layer that can observe it has a passing
test. Code inspection never sets a status (`compatibility.json`).

| Layer | What it proves | Command | Where it runs |
|---|---|---|---|
| Unit and API | engine logic, state machine, policies, networks, events, panel security rules, API wire format | `THOTHDOCK_TEST_PROOT=<proot> go test -race -count=1 ./...` | host, CI `test` |
| Runtime | real PRoot: process groups, signals, TTY, kill-tree | same, with `THOTHDOCK_TEST_PROOT` set (otherwise those tests skip) | host, CI `test` |
| PRoot patch | `--net-ip` rules on a static binary, two guests, gateway, refusals, nothing left running | `AndroidThothTerm/tests/garden-common/proot/net-ip-check.sh` | host, CI `nextgen` |
| Docker CLI end to end | lifecycle, exec, volumes, ports, Engine Guard | `tests/smoke/docker-cli-smoke.sh`, `tests/regression/lifecycle.sh` | host, CI `smoke` |
| Restart policies | policy semantics with SIGKILL and SIGTERM daemon cycles | `tests/regression/restart-policies.sh` | host, CI `nextgen` |
| Networks | addresses, names, aliases, -p, gateway, connect/disconnect, no LAN exposure | `tests/regression/networks.sh` | host, CI `nextgen` |
| Compose | MVP commands on a two-service stack, scoped down, attached up | `tests/regression/compose.sh [COMPOSE]` with upstream and Debian Compose | host, CI `nextgen` (runner's Compose) |
| Web Panel in a browser | pairing, actions, events, logs, CSP, phone width, clean stop | `tests/panel/e2e.sh` (Python Playwright + Chromium) | host |
| Performance | idle CPU/wakeups/RSS, latencies, throughput | `tests/perf/baseline.sh`, `tests/perf/compare.py` | host now; device PENDING |
| Android unit | Java integration, patch-series policy, product names | `./gradlew :garden-common:testDebugUnitTest :garden-debian:testFdroidDebugUnitTest` | host |
| Device | everything above that depends on Android: SELinux, `pidfd_getfd`, low ports, lifecycle, the real workloads | `tests/device/*.sh` with an isolated QA package | SM-A165F, owner-assisted |

## Rules

- Device tests use an isolated package (`com.thothterm.debian.qa.nextgen`)
  and its own data root, `adb install --no-incremental`, and never touch
  `com.thothterm.debian` or its containers. `connectedAndroidTest` is never
  run (it uninstalls the app under test).
- Performance comparisons are medians of repeated, interleaved runs on a
  quiet machine (`PERFORMANCE_BUDGET.md`).
- Long-running tests (24 h, 48 h, battery) are recorded only when they have
  actually run for that time.
- A test that can leave processes behind must check that it did not.
