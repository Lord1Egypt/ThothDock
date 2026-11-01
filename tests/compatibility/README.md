# Container compatibility suite

Real workloads and small probes that show whether ThothDock runs a given
class of Linux software correctly, on a desktop host and on Android. A test
passes only on verified behaviour (queries answered, data read back after a
restart, a signal handled, a process gone), never because a container is
"Up".

```
matrix.json          the tests: image, digest, priority, what is expected
runner/run.sh        runs tests against one engine, writes evidence
runner/lib.sh        helpers for the test scripts
runner/report.py     evidence directories -> matrix.csv, matrix-results.json, summary.md
probes/              core-probe.c (process, memory, sync, fs, net), go-probe,
                     build.sh; ipc-probe.c lives in tests/regression
workloads/<ID>/      one test.sh per workload, plus its fixtures
reports/             committed summaries of runs
```

Evidence (logs) goes to `evidence/<timestamp>` unless `COMPAT_EVIDENCE` says
otherwise; it is not committed except as the summaries in `reports/`.

## Running

The engine is always named explicitly; `run.sh` refuses a socket that is not
a ThothDock engine, so a test can never reach a real Docker daemon.

```sh
probes/build.sh amd64 probes/bin            # once per architecture
COMPAT_DOCKER_HOST=unix:///path/to/thothdock.sock runner/run.sh P-CORE G-ALPINE C-REDIS
COMPAT_DOCKER_HOST=unix:///path/to/thothdock.sock runner/run.sh all
runner/report.py reports/<date> host=evidence/<run>
```

On Android the same scripts run under `run-as` of a debuggable QA package
(never the production one), with `COMPAT_DOCKER_CLI` set to the package's
`libdocker.so` and `COMPAT_PROBES` to arm64 probes.

## Rules the tests follow

- One test at a time; heavy databases never run together.
- Every name is `compat-<test>-<run>-*`; cleanup removes only those, and a
  test that times out is cut off and its containers removed.
- `--restart no`, no `-p` except one loopback-only check in D-NGINX, services
  on a user-defined network (each container has its own 127.77.x.y address,
  low ports are mapped), so nothing is exposed to the LAN.
- Passwords are fixed disposable test strings, never real credentials.
- Expected refusals of unsupported Docker features are not failures; a
  missing image or platform limit is BLOCKED with the reason.

## Status values

`PASS` every check passed; `FAIL` none passed or the test could not run;
`PARTIAL` some checks failed; `BLOCKED` an external reason (image not
pullable, platform limit) stopped it; `UNSUPPORTED_BY_DESIGN`,
`NOT_TESTED`, `NOT_APPLICABLE` as named.
