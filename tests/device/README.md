# On-device tests

Run on a phone with a ThothDock-enabled ThothTerm build installed
(`com.thothterm.debian[.rc.thothdock]`). Never commit real device identifiers.

| Script | Where it runs | What it does |
|---|---|---|
| `accept.sh` | inside the ThothTerm terminal | 71 CLI checks: version/info, pull, run, exec, volumes, `-p`, Engine Guard through `apt full-upgrade`, package managers, logs/stop/kill/wait, inspect/restart/prune, TTY, exit codes |
| `guard-hardening.sh` | inside the terminal (disposable guest) | 48 checks: real `apt-get`/`dpkg` attempts to install, downgrade, remove or replace the stock Docker Engine are refused; ordinary update/upgrade/install work; managed CLI and engine unchanged |
| `hostile.sh` | inside the terminal | malformed/oversized API bodies, traversal and malformed volume names, bind paths, privileged/UDP refusal, health after abuse |
| `ui.sh`, `uia.sh` | host, `adb` | guarded input (refuses unless the app is in front); `uia.sh` taps by element text |
| `lc.sh`, `state.sh`, `cli.sh` | host, `adb run-as` (debug build) | lifecycle state: daemon/worker pids, socket, pid file, listeners, containers |
| `snapshot-protected.sh` | host | SHA-256, path and mtime of the apps a test must not touch |

Set `ADB="adb -s <serial>"` to pick a device. `connectedAndroidTest` must not be
used: it uninstalls the app.
