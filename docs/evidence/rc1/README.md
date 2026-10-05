# Release candidate 0.1.0-rc.1 evidence — 2026-10-05

Device: Samsung SM-A165F, Android 16, unrooted, adb `install --no-incremental`.
App: ThothTerm Trixie 0.3.0-rc.1 (versionCode 299) in the isolated package
`com.thothterm.debian.rc.thothdock`, debug-signed test builds of the production
source configuration. ThothDock `04a3e80` + stock Docker CLI 29.8.1 built from
the `docker/cli` source tag, both inside the APK; Go 1.26.8.

| File | What it shows |
|---|---|
| `go-test-summary.txt` | `go test -race` with a real PRoot, 100 tests, 0 failed, 0 skipped |
| `device-acceptance.log` | `tests/device/accept.sh` typed into the ThothTerm terminal: **71 pass, 0 fail** (version/info/ps/images, pull alpine and debian, run, `uname -m` = aarch64, exec, volumes, `-p`, Engine Guard through `apt update` / `upgrade` / `full-upgrade` and forced engine install, apk and apt inside containers, logs/stop/kill/wait, inspect/restart/prune, interactive TTY, exit codes, missing command, stdin, **SIGTERM'd shell loop exits 143**) |
| `device-hostile-api.log` | `tests/device/hostile.sh`: **20 pass, 0 fail** (malformed and truncated JSON, 2 MB body, traversal / absolute / NUL / oversized volume names, bind paths, privileged and UDP refusal, health after abuse) |
| `device-lifecycle.log` | background 45 s, daemon `kill -9`, Android force-stop and relaunch, closing the last window, Exit, uninstall and reinstall; process, socket, pid-file and listener state around each |
| `protected-apks-before.txt`, `protected-apks-after.txt` | identical (SHA-256, path, mtime) for the installed production ThothTerm apps and PocketClaw |
| `screenshots/` | terminal banner with `docker ps`, and the Containers screen (store listing images; status bar removed) |

Containers screen (debug build, driven by element text through `uiautomator`):
Start, Stop, Restart, Logs, Shell and Delete (with confirmation) were each
driven on the phone and the bundled CLI (`tests/device/cli.sh`) agreed with the
screen after every action; the published port `127.0.0.1:18090→8080/tcp` is
shown on the card. The minified release build (R8, not debuggable) was then
installed over it with `adb install -r` and smoke-tested (`docker version`,
`docker run --rm alpine uname -m`, Engine Guard report, Containers screen).

## Corrections made on the way

- First-run welcome, consent and About text said "ThothTerm Trixie"; the overlay
  now names ThothDock everywhere, with a unit test that fails if a new base
  string is not overridden.
- A signalled workload reported PRoot's stale exit status (0 for a shell loop);
  the vpid-1 signal line is now authoritative (143), with a regression test.
- Three expectations in the new test scripts were wrong, not the daemon: an
  environment index, prune printing IDs as real Docker does, and the status
  codes for a nonexistent bind source (400), a traversal path (Go's router
  redirects, then 404) and UDP (501). They were fixed in the scripts with the
  daemon unchanged.

## Not claimed

The matrix ran on the debug build (needed for `run-as` log retrieval); the
release build got the smoke test above, not all 71 checks. Containers are not a
security boundary; a published port is reachable by any app on the device.
