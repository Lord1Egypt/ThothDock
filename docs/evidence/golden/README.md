# Golden candidate evidence — 2026-10-05

Device: Samsung SM-A165F, Android 16, unrooted, QA app
`com.thothterm.debian.qa.thothdock` only. Production packages were not
modified (see below). Docker CLI 29.8.1 (stock), ThothDock `09076cc`.

| File | What it shows |
|---|---|
| `go-test-summary.txt` | `go test -race` with a real PRoot: 99 passed, 0 failed, 0 skipped, 15 packages |
| `device-acceptance.log` | 63 CLI checks on the phone, **63 pass, 0 fail** (version/info/ps/images, pulls, run, exec, volumes, `-p`, Engine Guard through `apt update`/`full-upgrade`, apk/apt in containers, logs/stop/kill/wait) |
| `device-lifecycle.log` | state snapshots around force-stop, relaunch, daemon `kill -9`, Exit, background, uninstall/reinstall, socket permission from the shell uid |
| `screenshots/` | Containers screen, delete confirmation, logs, graphite menus, Exit dialog, CLI/UI agreement, after-relaunch. A floating chat-head from another app was on screen; it is covered by a dark rectangle in every copy |
| `production-apks-before.txt`, `production-apks-after.txt` | SHA-256, path and mtime of the other installed ThothTerm apps and PocketClaw, before and after: identical |

## Lifecycle matrix (all on the phone)

`device-lifecycle.log` is the raw transcript. It contains two invalid background attempts that are kept for honesty and repeated validly later in the file: the first used a wrong activity name (`Error type 3`, app never launched) and the second used a container command that does not exist in Alpine (`httpd: applet not found`, exit 127, which the engine reported correctly).

| Scenario | Result |
|---|---|
| Background 45 s with a running container and published port | PASS: daemon, container and port unchanged |
| Closing the last terminal window (menu, confirmed), then reopening the app | PASS, with a documented semantic: closing the last window ends the session like Exit, so the daemon and its running container stop (no orphan process, socket removed, listener released); reopening starts one fresh daemon. Containers do not outlive the app |
| Shell action on the Containers screen | PASS: the terminal receives `docker exec -it <name> sh` and the shell runs inside the container |
| Force-stop, then relaunch | PASS: daemon and container PRoot gone immediately; stale socket replaced after identity check; running container reconciled to `Exited (137)`; published port free |
| Daemon `kill -9` with a running container | PASS: container PRoot dies with it, listener released, daemon restarted by the supervisor in about 2 s, CLI works at once |
| Exit (menu, confirmed) | PASS: no `libthothdock`, no PRoot worker, socket removed, no service record, listener released |
| Uninstall, then reinstall the golden APK | PASS: no data directory, process or listener left; fresh install boots to the ThothDock banner, `docker version` and `docker run alpine uname -m` give `aarch64`; Engine Guard is installed automatically |
| `adb shell` (uid 2000) opening the API socket | PASS (denied): `Permission denied` on directory and socket |
| Published port from `adb shell` | reachable, as designed: Android loopback is device-wide (SECURITY_MODEL threat F) |

## Containers screen

Start, Stop, Restart, Logs, Delete (with confirmation) and Shell were each
driven on the phone and each agreed with `docker ps -a` afterwards
(`screenshots/cli-matches-ui.png`). A client bug found on the way (two-write
requests giving `EPIPE` and "Engine offline") was fixed in the golden build
and soaked for 150 s with no new failures.

## Golden APK

| | |
|---|---|
| File | `ThothTerm-ThothDock-Golden-QA.apk` (not committed; attached to the review PR description by hash) |
| applicationId | `com.thothterm.debian.qa.thothdock` |
| versionName / versionCode | `0.2.2-thothdock-golden.1` / `202901` |
| Size | 93190419 bytes |
| SHA-256 | `a467ada2a2399e7ea0f1fab274948c66665a9c0db5d577161fbeb46cec63d9c4` |
| Signing | APK Signature Scheme v2, **Android Debug** certificate (`C=US, O=Android, CN=Android Debug`), SHA-256 `15cf75f9945d5354e75707e0326b7cffc60ac51a68df38156db318ef4578a27c`. It is a QA key, not a release key |
| Source | AndroidThothTerm `feature/thothdock-golden` built from `f20a095` (later commits only add docs); ThothDock `09076cc` (Go sources unchanged since; later commits are docs and evidence) |
| Reproducibility | Two clean builds have **identical contents** (same entries, every CRC and size equal) but not identical bytes: the Android Gradle plugin orders the `classesN.dex` entries differently from run to run, which moves offsets and the signing block (93190419 vs 93171218 bytes). The ThothDock daemon and the Docker CLI inside are byte-identical (pinned commit, pinned CLI tarball hash) |

## Known limitations observed

- A SIGTERM'd shell loop can report exit code 0 rather than 143 (shell behaviour in the tested loop, not the engine).
- No copy-up of image content into a new named volume; image `VOLUME`s are not made anonymous volumes.
- No UDP publishing; the workload's own bind address is not controlled (SECURITY_MODEL threats F and G).
- `docker exec` processes are not in the container's PID view (no PID namespace).
- The LAN page of the app does not use the new selection and ANSI-blue roles.
- The Gradle build with the ThothDock overlay needs network access for compile-time dependency resolution.
- Containers do not survive the app: when the app stops, containers stop.
