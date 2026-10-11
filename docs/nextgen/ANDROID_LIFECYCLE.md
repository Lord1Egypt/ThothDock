# ThothDock on the Android lifecycle

What happens to the engine, its containers and the Web Panel at each Android
event, and what restart policies can and cannot do. "Measured" rows have
device evidence (v0.1.1, `docs/evidence/rc1`); the rest are design
statements to verify in the P3-04 / P7 device rounds.

| Event | Engine | Containers | Web Panel | Evidence |
|---|---|---|---|---|
| App opened (first terminal) | `ThothDock.ensureRunning()` starts it as a child of the app | restart policies restore eligible containers (ADR-0004) | off | engine start measured; restore PENDING on device |
| Screen off, app in background | keeps running under the `specialUse` foreground service | keep running | keeps running if started | owner observation: 4 containers ~2 days with the screen off and a game in the foreground |
| Another app in the foreground | unchanged | unchanged | unchanged | owner observation (Wild Rift) |
| Daemon crash | the supervisor restarts it (bounded) | killed with it (`Pdeathsig`, `--kill-on-exit`); restored by policy at restart | keeps running, shows "engine unreachable", recovers | supervisor measured; policy restore host-tested |
| Last terminal window closed / Exit | `ThothDock.stop()`: SIGTERM, then identity-checked SIGKILL | stopped (not a manual stop, so policies apply at the next start) | stopped with the engine | measured (v0.1.1) |
| Android kills the app (memory, OEM battery manager) | gone | gone (SIGKILL chain) | gone (`--exit-with-parent`) | measured (force-stop) |
| Force-stop | gone; stale socket/pid cleaned at next launch | gone; restored by policy only when the app is opened again | gone | measured (v0.1.1) |
| Device reboot | not started (no boot receiver yet) | restored the next time the app is opened | off | Server Mode (P7-01) will add an owner-enabled boot start |
| App update (`adb install -r`, store update) | the app process is replaced | as force-stop | as force-stop | — |
| Uninstall | data root deleted with the app | deleted | deleted | measured |

## Android's phantom-process limit (measured 2026-10-10)

Android 12+ caps the number of child ("phantom") processes **across all
apps on the device** (default 32) and, past the cap, kills the oldest app's
children: `am_kill ... Killing PhantomProcessRecord 10918:libthothdock.so ...
Trimming phantom processes`. Every container is at least a PRoot process plus
its workload, and the engine, terminal shells and `docker exec` add more.
A second ThothTerm app running its own engine pushed the SM-A165F over the
cap and Android killed the **production** engine, which stopped its four
containers (they had no restart policy). So: the golden four-container run is
safe only while the device-wide total stays under the cap; a second engine
or many containers can lose the older group without warning.

Mitigations, in order of preference (tickets P3-05, P7-03):
1. a restart policy on every long-running container, so they come back when
   the engine restarts (Android restarts the foreground service);
2. never run two ThothDock engines at once (QA packages are stopped after a test);
3. the owner may raise the limit (adb, reversible): `adb shell device_config put
   activity_manager max_phantom_processes 2147483647` and
   `adb shell settings put global settings_enable_monitor_phantom_procs false`;
   Samsung may reset these on reboot;
4. engine-side: fewer processes per container is not possible under PRoot
   (loader + tracee); the engine will report its process count to the panel.

### The budget, measured again on 2026-10-11

| State | Processes of the app's uid |
|---|---|
| The owner's four containers (TON API, TON explorer, two small fixtures), engine, one terminal | **22** (nginx alone: 9 = master + 8 workers, one per CPU) |
| The same plus five small test containers and their `docker exec`/`run --rm` helpers | over the cap: Android logged `Killing PhantomProcessRecord … libthothdock.so: Trimming phantom processes` at 06:35:15 and the engine died with exit 137; the last terminal session ended and the app process exited (`evidence/pre-release/phantom/`) |

Recovery: opening the app restarted the engine and restored every
`unless-stopped` container within seconds; containers without a policy stayed
exited, as in Docker. What this means for users, until ThothDock warns by
itself (ticket QA-07):

- a workload's own process count matters more than its memory: prefer
  `worker_processes 1` (nginx), `-w 1` (gunicorn) and similar settings;
- about 30 processes is the practical ceiling for the whole app, terminal
  sessions included;
- device tests stop disposable fixtures first and remove test containers as
  soon as their checks are done (`tests/device/networks-device.sh`).

Force-stop (Settings > Force stop, or `am force-stop`) kills the engine and
every container at once; nothing runs again until the app is opened, which
is how Android treats a force-stopped app. Measured: all four
`unless-stopped` containers were running 4 s after the next launch, and the
stale pid file left behind was handled.

## Rules this design keeps

- **No wake locks** are taken by the engine or the panel. Long-running work
  relies on the foreground service the terminal already runs.
- **No polling.** Restart policies react to process exit; the Containers
  screen polls only while it is visible; the Web Panel is event-driven.
- **Nothing outlives the app.** The engine, every container process and the
  panel are descendants of the app process with a parent-death signal; a
  force-stop or kill leaves nothing running.
- **Force-stop is final.** Android's force-stop is the user's or the
  system's decision; restart policies do not try to defeat it.

## Server Mode (planned, P7)

An owner-enabled setting that (1) starts the foreground service at boot
(`RECEIVE_BOOT_COMPLETED`; the `specialUse` service type may be started
from a boot receiver on Android 15+, which forbids it only for dataSync,
camera, mediaPlayback, phoneCall, mediaProjection and microphone types —
to be confirmed on the device), (2) starts the engine, and (3) asks the
owner to exempt the app from battery optimisation, explaining the trade.
It changes nothing unless enabled.
