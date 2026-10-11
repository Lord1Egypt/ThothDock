# ThothDock 0.2.0 / ThothTerm • ThothDock 0.4.0 (NextGen)

Engine 0.2.0 inside the Android app ThothTerm • ThothDock 0.4.0 (versionCode
400, package `com.thothterm.debian`). Upgrades 0.1.1 / 0.3.1 in place; existing
containers, images and volumes are kept (verified with a data root written by
the published v0.1.1: `tests/regression/upgrade-from-v0.1.1.sh`).

## What is new

- **User-defined networks:** each container gets its own 127.77.x.y address, names and
  aliases through `/etc/hosts`, a private `localhost`, outbound DNS and HTTPS.
- **Restart policies** (`no`, `always`, `unless-stopped`, `on-failure`) with Docker's
  semantics and backoff, restored when the app opens; **Docker events**.
- **Docker Compose v2** (installed from apt by the app on first use) runs real stacks.
- **Web Panel:** HTTPS, one-time pairing code, in the app's Containers screen next to
  Images and Volumes. **A paired browser stays authorised only while that panel process
  runs** (12 h idle at most): Stop or any restart makes every browser pair again, and
  "Disconnect all devices" ends them all at once.
- **Process Guard:** warnings in `docker info`, `docker create`, the Containers screen
  and the Web Panel when the app's process count is high (Android can end the engine
  and every container when too many processes exist), and a recovery report after an
  unclean exit.
- Fixes: DNS/outbound from user networks, underscores in aliases, `docker restart` no
  longer counts as a policy restart, commands inside bind mounts, Host-header check.

## Read this before relying on it

- **Not isolation.** Containers run under PRoot in the app's own identity: no Linux
  namespaces, cgroups or seccomp. User-defined networks separate names and addresses,
  they do not enforce network isolation. Do not run untrusted workloads.
- **Android can still end the engine** when the device-wide process cap is passed
  (about 22 processes ran for hours; five more containers ended the engine). Process
  Guard warns; it cannot prevent this. Use `--restart unless-stopped`.
- **LAN Mode** (ThothTerm's LAN terminal) is plain HTTP on the local network: its PIN
  authenticates but nothing is encrypted.
- **Web Panel** certificate is self-signed: compare the fingerprint shown in the app.
- **Reliability beyond one hour is unverified in controlled endurance testing.** The
  owner waived the 6, 24 and 48 hour soak tests. A one-hour soak with four workloads
  passed (61/61 samples); a Wild Rift gaming session was reported by the owner as
  normal (duration and performance not measured).
- Network-disconnection behaviour was not tested (the test device is driven over Wi-Fi).

## Rollback

Android refuses to install a lower versionCode over a newer one for a release
app, so going back from 0.4.0 to 0.3.1 (signed by the same certificate) means
uninstalling first, which **erases `/home/thoth` and the app's data**: copy what
you need out first. The engine data root itself is readable by v0.1.1, but it
ignores restart policies and user networks (a container on a user-defined
network would start on the device network); remove those containers before
rolling back (`RELEASE_STRATEGY.md` §6). Published tags are never moved: a fix
ships as a new version.
