# Flatpak and Snap on ThothDock: design and verified constraints

Status: design, 2026-10-31. Nothing here is implemented yet. Every
"verified" line below was run on the owner's Samsung SM-A165F (Android 16,
kernel 6.12.38, production ThothTerm • ThothDock 0.4.1) or in a disposable
Debian trixie container on the host, and says which.

## Goal

`flatpak install flathub <app>` and `snap install <app>` work in the Debian
guest without root, the way `docker run` already does: the stock command line,
a userspace engine behind it, and the same Garden PRoot underneath. Phase 1
covers command-line and server apps. GUI apps need a display, which ThothTerm
does not have (Phase 3).

## What was verified

| Question | Result | Where |
|---|---|---|
| Can the app create user namespaces? | Yes: `unshare -Ur true` exits 0 | device |
| Does Debian's bubblewrap 0.12.0 run in the guest? | No: `Can't read /proc/sys/kernel/overflowuid: Permission denied` (SELinux on `/proc/sys`) | device |
| Does bubblewrap run under PRoot at all, with `/proc/sys` readable? | No. Without `-0`: `setting up newroot bind: No such file or directory`; with `-0`: `setting up uid map: Operation not permitted`. Directly (no PRoot) it works | host container |
| Can a sandbox start a second PRoot inside the guest? | No: the guest's PRoot already traces every descendant, and a process has one tracer | by construction |
| Can stock snapd run? | No: it needs systemd as PID 1, a setuid `snap-confine`, squashfs mounts and AppArmor | snapd's own requirements |

Consequences:

1. Flatpak can stay unmodified, but `bwrap` cannot. Flatpak honours the
   `FLATPAK_BWRAP` environment variable, so a bwrap-compatible shim in the guest
   can stand in for it.
2. A sandbox must be started from outside the guest, like a container: the
   shim asks ThothDock over the socket it already serves, and ThothDock starts a
   separate PRoot with the right binds.
3. Snap needs its own small daemon side, as Docker did: the stock `snap`
   client talks to an engine that implements only what an unconfined, rootless
   snap needs.

## Flatpak (Phase 1)

```
guest:  flatpak --user install flathub org.example.Tool     (unmodified Debian flatpak)
        flatpak run org.example.Tool
          └─ FLATPAK_BWRAP=/usr/libexec/thothdock/bwrap  (shim, parses bwrap argv)
               └─ POST /thothdock/sandboxes  over /run/thothdock/thothdock.sock
engine: new empty root dir + PRoot -r <dir> -b <binds…> --link2symlink, argv, env, cwd
        stdio and exit status relayed like `docker run -i`
```

* `--user` installs only. OSTree pulls are HTTPS and verified by OSTree with
  Flathub's GPG key, which `flatpak remote-add` imports from the `.flatpakrepo`
  file. Checkouts land in `~/.local/share/flatpak` inside the guest.
* The shim must accept the options flatpak actually passes: `--args FD`,
  `--bind/--ro-bind/--dev-bind[-try]`, `--symlink`, `--dir`, `--tmpfs`,
  `--file FD DEST`, `--bind-data FD DEST`, `--ro-bind-data FD DEST`,
  `--setenv/--unsetenv`, `--chdir`, `--proc`, `--dev`, `--info-fd`,
  `--sync-fd`, `--block-fd`, `--seccomp FD`, `--die-with-parent`,
  `--new-session`, `--as-pid-1`, `--unshare-*`, `--share-net`, `--cap-*`,
  `--perms`, `--size`. Data passed on descriptors becomes files in the
  sandbox root. Namespace, seccomp and capability options cannot be honoured
  and are dropped, which is reported as a warning, never silently.
* `--info-fd` must still receive `{"child-pid": N}`, or `flatpak ps` and
  `flatpak kill` break.
* Guest paths. Flatpak names guest paths (`/home/thoth/.local/share/flatpak/...`),
  but the engine runs in the app's context, and on Android it is started
  without `--allow-bind`, so it cannot bind arbitrary host paths. The daemon gets
  a `--guest-root DIR` (the Debian rootfs, passed by the app), and every source
  path of a sandbox is resolved inside it with chroot semantics
  (`internal/securefs`), so a guest symlink cannot point a bind outside the
  guest. Sources outside the guest root are refused.
* Hard links. OSTree checks files out as hard links. In the guest those are
  PRoot link2symlink entries whose symlinks hold absolute host paths; the
  sandbox's PRoot also runs with `--link2symlink` and resolves them the same way.

## Snap (Phase 2)

```
guest:  snap install hello-world      (stock snap client, built from source like the Docker CLI)
          └─ snapd REST subset over /run/snapd.socket, served by the engine
engine: Snap Store API v2 (api.snapcraft.io) → .snap download → sha3-384 check
        → unsquashfs → base snap (core22/core24) as the root, the snap at
        /snap/<name>/<rev>, SNAP* environment → PRoot, as for a sandbox
```

* Only `confinement: classic` behaviour is possible: no AppArmor, seccomp or
  mount namespace. `snap install` of a strict snap installs it unconfined and
  says so, in the same way that containers are documented as "not a security
  boundary".
* Verification: the store's `sha3-384` over HTTPS first; then the full
  assertion chain (account-key → snap-declaration → snap-revision) against
  the pinned store root key. A snap whose hash does not match is refused.
* Squashfs: snaps are xz-compressed squashfs and Go's standard library has
  neither. The engine runs the guest's `unsquashfs` (Debian squashfs-tools) in a
  helper PRoot instead of vendoring a decompressor.
* Engine Guard refuses the real `snapd` package, as it does dockerd.
* The Snap Store backend is not free software. F-Droid will likely want the
  `NonFreeNet` anti-feature for it; Flathub's server side is free.

## Panel decision (Containers screen and Web Panel)

Yes, with limits. Sandboxes and snaps run in the same engine, so the Containers
screen gets an **Apps** tab next to Containers/Images/Volumes. It lists
installed Flatpak and Snap apps with Run, Stop, Remove and their size on disk,
and the Web Panel shows the same list. It adds no background work: it reads
the engine only while the screen is open, reusing the existing 3 s refresh.
Installing stays in the terminal, as pulling images does. It ships only after
Phase 1 works on the device.

## Tests required before release

* Stability: a 1 h soak with a Flatpak CLI app and two containers running;
  engine restart; app force-stop; uninstall of an app while it runs.
* Conflicts: Engine Guard still refuses dockerd, containerd, runc and snapd;
  `docker` and `flatpak` at the same time; Process Guard counts sandbox
  processes, which also count towards Android's phantom-process cap.
* Resources: idle CPU, wakeups and RSS of the engine with and without a
  sandbox (A/B, as for 0.2.0); download size, on-disk size (OSTree dedupe under
  link2symlink) and peak RSS for one runtime and one app.
* Host suite: the shim's argv parser against bwrap's own option table, and a
  replay of real `flatpak run` command lines captured with
  `FLATPAK_BWRAP=/usr/bin/echo`.

## Phase 3 (not planned yet)

GUI apps: an X11 or Wayland server rendered to the Android app or to the
browser (Web Panel), for example with Xvfb plus a VNC or Xpra HTML5 client.
That is a separate design.
