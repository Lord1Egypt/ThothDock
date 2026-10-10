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
