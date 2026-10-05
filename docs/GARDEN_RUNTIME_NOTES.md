# Garden runtime notes

These notes cover what ThothDock learned from the ThothTerm Garden runtime and
from experiments on a real phone. The Garden repository (`~/AndroidThothTerm`)
was read at `b7f4936` (origin/master has since moved to `17cde8b`, which
contains it) and was **not modified**. Nothing here depends on the Android
application's code: ThothDock reuses concepts and Garden's PRoot binary, not
Java classes.

Status labels: **MEASURED** means observed in this round on the device or the
host. **READ** means taken from Garden's source or documentation.
**UNVERIFIED** means a stated expectation that has not been tested yet.

## 1. How Garden runs PRoot (READ)

`garden-common/.../GardenRuntime.java` builds every PRoot command line:

```
libproot.so --rootfs=<canonical rootfs> --root-id --link2symlink --cwd=<dir>
            (--hangup-on-exit | --kill-on-exit) --kernel-release=6.1.0-thothterm
            --bind=/dev --bind=/proc --bind=/sys --bind=/proc/self/mounts:/etc/mtab
            [--bind=<private resolv.conf>:/etc/resolv.conf] <command>
env: PROOT_TMP_DIR, PROOT_LOADER, LD_LIBRARY_PATH=<runtime lib dir>, HOME, PATH, TERM, ...
```

- Terminal windows use `--hangup-on-exit` (patch 0004), so `nohup` jobs survive.
  One-shot provisioning uses `--kill-on-exit`. A container must never outlive
  its process, so ThothDock always uses `--kill-on-exit`.
- PRoot passes its own environment to the guest. ThothDock therefore starts
  PRoot with exactly the container's environment plus `PROOT_TMP_DIR`,
  `PROOT_LOADER` and `LD_LIBRARY_PATH`. The library directory is prepended to
  any `LD_LIBRARY_PATH` the image sets, because PRoot itself needs it.
- `--root-id` is fake root. ThothDock uses it for uid 0 and `--change-id=U:G`
  for `--user`.
- `--kernel-release` is a constant in Garden. ThothDock reports the real
  kernel unless `--kernel-release` is given.

## 2. Binaries and libraries (READ, MEASURED)

| Piece | Location on the device | Notes |
|---|---|---|
| `libproot.so` (the proot executable) | `nativeLibraryDir` (`/data/app/~~…/lib/arm64`) | world-readable; built per edition |
| `libproot_loader.so` | `nativeLibraryDir` | its path is compiled into PRoot as `/data/data/<app id>/files/linux/runtime/loader/loader`; `PROOT_LOADER` overrides it |
| `libtalloc.so.2`, `libandroid-shmem.so` | `files/linux/runtime/lib` (app-private) | **both** are `DT_NEEDED` by `libproot.so` (MEASURED: without shmem, `CANNOT LINK EXECUTABLE … libandroid-shmem.so not found`) |
| rootfs | `files/linux/<distro>/rootfs` | extracted by `TarballExtractor` |

Garden's PRoot is termux/proot `7266fb3e` (tag v5.1.107.92) plus patches
0001–0008. Its `--version` prints a git description
(`trixie-v0.2.0-71-g54a965fc-dirty` on Trixie 0.2.1), not a number.

## 3. Process lifecycle lessons ThothDock adopted (READ)

- **`destroyForcibly()` is SIGTERM on Android.** PRoot survives SIGTERM while
  a traced child hangs. ThothDock's `Kill` sends SIGKILL to PRoot, and
  `--kill-on-exit` plus `PTRACE_O_EXITKILL` take every tracee with it.
  MEASURED on the host: a SIGTERM-ignoring workload with a background child
  ends with code 137, and the child does not survive (`TestPRootKillTakesTheWholeTree`).
- **Stop** sends the stop signal to the process group (PRoot and workload),
  waits, then SIGKILLs. PRoot ignores SIGTERM itself and reports a
  signal-killed workload as exit 255 plus a line
  `proot info: vpid 1: terminated with signal N`. ThothDock removes that line
  from the container's stderr and reports `128+N`, as Docker does (MEASURED).
- **Android app processes run with SIGHUP ignored**, and that disposition
  survives `fork`/`exec`. A daemon started by the app will inherit it, and so
  will containers. Open item for the integration: reset SIGHUP to its default
  for container processes (UNVERIFIED in app context).
- A missing command makes PRoot print `proot error: 'x' not found` and exit 1.
  ThothDock resolves the executable itself through the container's `PATH`
  first, so `docker run` reports Docker's message and exit code 127.

## 4. Filesystem lessons (READ, MEASURED)

- **Hard links are refused to apps** (SELinux
  `neverallow all_untrusted_apps file_type:file link`). Garden's extractor
  copies instead. MEASURED: the shell domain in `/data/local/tmp` is refused
  too. ThothDock's extractor and per-container copy fall back to copying, and
  `--link2symlink=auto` enables PRoot's link emulation inside containers when
  the data root refuses links.
- Garden's extractor security model (strict names, never extract through a
  symlink, `O_NOFOLLOW|O_EXCL` final components, validated hard-link targets,
  deferred directory modes, no setuid/setgid/sticky) is the basis of
  ThothDock's `internal/securefs` and `internal/layer`. One deliberate
  difference: OCI layers may legitimately write *through* a symlinked parent
  (for example `bin -> usr/bin`). ThothDock resolves such parents with chroot
  semantics, one directory file descriptor at a time, so they stay inside the
  root, instead of rejecting them.
- There is no OverlayFS. Each container gets a private copy of the image
  rootfs; alpine is about 8 MB and debian:trixie-slim about 101 MB.

## 5. Networking (READ)

LAN Mode binds only RFC 1918 addresses on Wi-Fi or Ethernet, and each edition
has its own port (7681–7684, next 7685; `docs/garden/PORTS.md`). ThothDock
publishes nothing by default. A later `-p` implementation will bind
`127.0.0.1` unless LAN exposure is explicitly requested. Containers already
share the device network: a service listening on port N inside a container is
reachable at `127.0.0.1:N`.

Garden writes a private `resolv.conf` from Android's `LinkProperties`
(`AndroidNetworkResolver`). ThothDock takes it with `--resolv-conf` both for
its own DNS (Android has no `/etc/resolv.conf`) and for containers.

## 6. Device experiments, 2026-10-04 (MEASURED)

The device was a Samsung SM-A165F on Android 16 (SDK 36), kernel 6.12.38, arm64,
unrooted, with 4 KB pages. ThothDock linux/arm64 (static Go) ran from
`/data/local/tmp/thothdock` as the `shell` user, with **copies** of Trixie
0.2.1's `libproot.so` and `libproot_loader.so` and the same-version
`libtalloc.so.2` and `libandroid-shmem.so`. The installed editions are release
builds (`run-as: package not debuggable`), so no app context was available
and no app data was touched.

| Experiment | Result |
|---|---|
| `thothdock doctor` | PASS after adding `libandroid-shmem.so`; hard links refused; `/dev/ptmx` usable; a traced process ran under Garden's PRoot |
| Unix socket in `/data/local/tmp` (`shell_data_file`) | **`bind: permission denied`** in the shell domain. The spike used `--socket none --dev-tcp 127.0.0.1:23750` (a development flag) instead |
| TLS from a `GOOS=linux` binary | Failed (`x509: certificate signed by unknown authority`) until ThothDock pointed Go at `/apex/com.android.conscrypt/cacerts:/system/etc/security/cacerts` (`platform.UseAndroidTrustStore`) |
| Docker CLI 29.8.1 linux/arm64 → ThothDock | `version`, `info`, `ps`, `images` OK; API negotiated to 1.41 |
| `docker pull alpine:3.20`, `debian:trixie-slim` | OK; arm64 selected from the manifest lists |
| `docker run --rm alpine echo …`, `uname -m` = `aarch64` | OK |
| `apk add curl` + HTTPS request | OK |
| `apt-get update && apt-get install curl ca-certificates` in Debian | OK, 52 s |
| `docker run -it debian bash` | prompt, `stty size` = 25 90, exit 5 propagated |
| Two containers from one image | independent; the image rootfs is unchanged |
| Process tree | `thothdock → libproot.so → sh → sleep`; zero `dockerd`, `containerd` or `runc` processes |

### Daemon placement: inside a guest (nested PRoot) or next to Garden?

The brief asked for this to be decided by experiment. The nested model failed
in three independent ways:

1. **Garden's PRoot cannot start inside a Linux guest.** It is a bionic
   executable. Android's linker inside the guest cannot read
   `/linkerconfig/ld.config.txt` (PRoot: `can't sanitize binding
   "/linkerconfig": Permission denied`) and then fails on unrelated system
   libraries (`libicu.so not found`).
2. **A guest-native PRoot is not suitable.** Debian trixie's `proot` 5.1.0
   lacks `--kill-on-exit` (ThothDock now refuses such a PRoot at startup).
3. **Nested ptrace breaks execution.** Debian's PRoot run as a tracee of
   Garden's PRoot fails with `execve("/bin/sh"): No such file or directory`
   for every program.

**Decision: the daemon runs in the Android application's context, beside
Garden's PRoot, and is never nested.** Every successful phone result above
used that shape: ThothDock spawning Garden's `libproot.so` directly.

## 7. Garden integration — MEASURED (2026-10-05)

The plan in the previous revision of this file was built and proven on the
SM-A165F with an isolated debuggable QA app, `com.thothterm.debian.qa.thothdock`
(AndroidThothTerm branch `qa/thothdock-integration`). No adb shell runs the
acceptance commands: they are typed into the app's own terminal.

```
Android app context (u0_aNNN, one uid)
 ├─ ThothTerm service ── supervises ──▶ libthothdock.so serve   (outside the guest)
 │                                        │  Unix socket  files/thothdock/sock/thothdock.sock  (srw-------)
 │                                        └─ spawns Garden PRoot ─▶ container rootfs
 └─ terminal session: libproot.so … --bind=<files/thothdock/sock>:/run/thothdock
                                     --bind=<nativeLibraryDir>/libdocker.so:/usr/local/bin/docker
        └─ Debian guest: DOCKER_HOST=unix:///run/thothdock/thothdock.sock ── docker CLI ─┘
```

- **Packaging.** `libthothdock.so` (built from the pinned ThothDock commit with
  `git archive`, `-trimpath`, empty build id; two builds were byte-identical)
  and `libdocker.so` (the stock Docker 29.8.1 static `docker` binary, extracted
  alone from the official tarball and checked against two SHA-256 pins; no
  `dockerd`, `containerd` or `runc`) sit in `nativeLibraryDir` beside
  `libproot.so`. They are staged only for builds with a QA application id and
  an explicit ThothDock source property.
- **Unix sockets work in the app context.** The app's `app_data_file` domain may
  create sockets (the `shell` domain could not, §6). The socket path is about
  100 bytes (limit 107). The guest reaches it through one bind-mounted
  directory that holds only the socket.
- **Supervision** (`com.thothterm.dock.ThothDock`): single-flight start on a
  supervisor thread (never the UI thread), readiness by `/_ping`, restart with
  backoff, stop = SIGTERM then SIGKILL, pid file for a daemon left by a
  restarted app, `--exit-with-parent` (PR_SET_PDEATHSIG). The terminal never
  waits for it; the notification shows `ThothDock: running/unavailable`.
- **Branding** is an overlay enabled only for ThothDock QA builds
  (docs/branding/VISUAL_IDENTITY.md); the banner shows `Engine ThothDock` or
  `Engine Offline` from the live socket, with nothing run at startup.

### Lifecycle results (SM-A165F, evidence in docs/evidence/garden-integration/)

| Scenario | Result |
|---|---|
| App start → daemon start → terminal | daemon ready in well under a second; `docker version` from the terminal reports `Server: ThothDock … d5d3d04` |
| Background 20 s, foreground | daemon and shell stayed up; `docker ps` worked |
| Shell `exit` (closes the app) and reopen | daemon stopped gracefully and the socket was removed; reopen started a new daemon |
| Daemon SIGKILL while a container runs | the container process died with it (PDEATHSIG, no orphan); a new daemon and a fresh socket in about 1 s; the container is recorded `exited 137`, not "running" |
| `am force-stop` (Android process kill) with a container | every process died, no orphan. **A stale socket file and pid file remain until the next launch**, when they are replaced; nothing can connect to them meanwhile |
| Menu → Exit with a running container | graceful: the container got SIGTERM (exit 143), daemon logged `stopped`, socket and pid file removed, no QA-uid process left |
| Uninstall the QA app | no `libthothdock`/`libdocker` process, package gone |
| Update-in-place over a running app | the old daemon ended, the new APK started a fresh one; existing containers and images persisted |
| Production apps | five installed packages (devel, ubuntu, debian, arch, PocketClaw) have identical APK SHA-256 and timestamps before and after |

Not covered: a daemon crash loop in the field (the restart policy gives up after
3 failures a minute and shows `unavailable`), Android's low-memory killer, and
Doze with the screen off for long periods.

## 8. Golden candidate round — MEASURED (2026-10-05)

Device: Samsung SM-A165F, Android 16, unrooted. QA app
`com.thothterm.debian.qa.thothdock`; no production package touched.

### Process model

```
Android app (uid u0_aNNN)
 ├─ TermService ── supervisor thread ── libthothdock.so serve   (daemon, app context)
 │                                        ├─ /files/thothdock/sock/thothdock.sock (0600)
 │                                        ├─ container PRoot  (Pdeathsig = daemon)
 │                                        ├─ exec PRoot       (second tracee, same rootfs)
 │                                        └─ port forwarders  (goroutines, 127.0.0.1)
 ├─ guest shell PRoot ── docker CLI ──▶ /run/thothdock/thothdock.sock (bind mount)
 └─ Containers screen ── LocalSocket ──▶ same socket (same API, no private state)
```

### Lifecycle facts (each observed on the device)

| Event | Result |
|---|---|
| App backgrounded 45 s with a running container and a published port | daemon, container and port unchanged; the port answered from `adb shell` |
| Force-stop of the app | daemon and container PRoot die with it (Pdeathsig, `--exit-with-parent`); the listener is released; the socket file is left behind and **replaced by the next start** after an identity check (pid + `/proc` start time) |
| Relaunch after force-stop | a single new daemon; a container that was running is reconciled to `Exited (137)`; its port is free |
| `kill -9` of the daemon while a container runs | the container's PRoot exits at once; the app supervisor restarts the daemon in about 2 s; the CLI works again immediately |
| Exit (menu, confirmed) | no `libthothdock`, no PRoot worker, socket and pid files removed, no service record, no listener |
| Shell uid (`adb shell`) opening the socket | `Permission denied` on the directory and on the socket |

### Why the daemon is not inside the guest

Nested PRoot does not work on this device (documented in §6), so the daemon runs
next to the guest PRoot and the socket directory is bind-mounted into the guest.

### Update model

ThothDock ships inside the app: updating the app updates the daemon. The Docker
CLI is the unmodified static 29.8.1 binary pinned by SHA-256. In Debian/Ubuntu
guests the Docker **client** packages remain upgradable with apt; the engine
packages are held by Engine Guard (see COMPATIBILITY.md).

### Root semantics

"root" inside a container is PRoot's fake root (`--root-id`); it grants no
Android privilege. `--user` changes the faked identity only.
