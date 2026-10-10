# Failure recovery

Every recovery primitive, what triggers it, and the test that proves it.

| Failure | Recovery | Test |
|---|---|---|
| Daemon killed (SIGKILL, Android kill) while containers run | Containers die with it. At the next start `reconcile` kills any survivor whose PID **and** start time match, records `exited (137)` with an explanation, then restart policies restore eligible containers | `TestStaleRunningStateIsReconciled`, `TestRestoreAtDaemonStart`, `restart-policies.sh` (SIGKILL cycle), `networks.sh`, `compose.sh` |
| Daemon stopped cleanly (SIGTERM, Exit) | Containers get their stop signal, then SIGKILL after the timeout. A shutdown stop is not a manual stop | `TestShutdownIsNotAManualStop`, `restart-policies.sh` |
| Container crash loop | Backoff 100 ms → 60 s; a 10 s run resets it; `stop`, `kill -9` or `rm -f` cancels the pending restart | `TestNextRestartDelay`, `TestRestartingContainerStopKillAndRemove`, `restart-policies.sh` |
| A policy start fails (missing binary, port taken) | Counted as a failed run: the policy decides again with a longer delay | `startFailed` path, `TestOnFailureRetriesUpToTheLimit` |
| Interrupted create | The record is written last; a directory without it is removed at start | `TestInterruptedCreateAndRemoveRecovered` |
| Interrupted remove | `removing` is finished at start | same |
| Interrupted pull | Staging in `tmp/` is emptied at start; nothing partial becomes an image | `internal/image` tests |
| A record says `restarting` after a crash | Reconciled to `exited`; the policy is applied again by the restore | `reconcile` (`StatusRestarting` case) |
| Network removed out of band (store edited, partial write) | Memberships of missing networks are dropped at start; the container returns its address when it has no network left | `loadNetworking` |
| Address conflict in records | The second owner gets a new address | `Pool.Reserve` conflict path |
| Stale socket and pid file after force-stop | Removed by the Android side before the next start, after checking that no live daemon of ours owns them | `ThothDockIdentityTest` (garden-common) |
| Web Panel crash | The panel is a separate process: the engine and containers are unaffected; the owner restarts it from the menu | process separation (ADR-0005) |
| Event subscriber too slow | Disconnected (its stream ends); the engine never blocks | `TestSlowSubscriberIsDisconnectedNotBlocking` |
| Illegal state transition (a bug) | Refused with 409 and logged, never applied | `TestTransitionGuardRefusesIllegalMoves` |

Recovery never restarts a container that was manually stopped while the
daemon ran, except an `always` container at daemon start, which is
dockerd's documented behaviour (ADR-0004).
