# Final hardening round evidence (2026-10-11)

Device: Samsung SM-A165F, Android 16, QA package `com.thothterm.debian.qa.nextgen`
(debug-signed, in-place installs; the owner's four containers restored by their
`unless-stopped` policy each time). No pairing code, cookie or CSRF token is stored.

| File | What |
|---|---|
| `session-lifecycle/device-lifecycle.log` | 24/24 on build 013ae9f: pair, Stop, Start, old browser 401, Disconnect all devices |
| `session-lifecycle/device-lifecycle-rc.log` | the same 24/24 on the release-candidate engine 508ac4e |
| `session-lifecycle/shots/` | desktop-browser screenshots: paired dashboard, the same tab after the restart (pairing required), paired again |
| `panel-security-session.log`, `panel-security-exhaust.log` | the 22 Web Panel security checks on the final build |
| `networks-device.log` | network semantics 26/26 |
| `restart-device.log`, `restart-device-run1-1fail.log` | restart policies 16/16; the first run (15/16) read an events window that closed in the crash's own second |
| `recovery-after-force-stop.json` | recovery report after `am force-stop` (unclean, 4 interrupted, 4 restored) |
| `pre-install-containers.txt` | the owner's containers before the first in-place install |
| `perf/` | idle cost: `baseline-pre-processguard.csv`, `final-rc-background.csv`, the intermediate build with the screen open/closed, `host-idle-ab.txt` |

`QA-CLOSURE-2026-10-10.md` explains each result and what was not run.
