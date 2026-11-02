# Flatpak and Snap on ThothDock: design and verified constraints

Status (2026-11-01): implemented and tested on the SM-A165F — Flatpak (bwrap shim, sandboxes, Apps tab), Snap (thothdock-snap: store installs with SHA3-384 check, bases, hooks, apps and services), the Apps tab in the Containers screen and Web Panel, and the stress/address test below. Not done: snap assertion verification, GUI apps (Phase 3).
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
  and are dropped; the engine logs them with every sandbox start and the shim
  prints them with THOTHDOCK_BWRAP_VERBOSE=1 (not by default, so every
  `flatpak run` does not print the same lines).
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
* `xdg-dbus-proxy` (0.4.1). An app whose manifest has `[System Bus Policy]`
  entries (KeePassXC, Firefox) makes flatpak start `xdg-dbus-proxy` through
  bwrap with `--args=FD` (the argument list, itself holding `--fd=N`: the write
  end of a ready pipe) and descriptors that cannot cross the engine socket, so
  the sandbox failed with "Failed to load --args / Failed to sync with dbus
  proxy". The shim now recognises the command, expands `--args=FD` inline,
  runs the proxy in the sandbox and writes the ready byte when its socket
  exists; when flatpak closes the pipe, the proxy is stopped.
* Device nodes. flatpak binds `/dev/dri` (and similar) with `--dev-bind-try`
  for GL apps; the guest has none and the sandbox's `/dev` is the runtime's,
  so a bind whose source and destination are the same path under `/dev` is
  skipped (it failed with `bind source "/dev/dri": file does not exist`).
* Commands that are symlinks (0.4.2). `/app/bin/libreoffice` is a symlink to
  `/app/libreoffice/program/soffice`: the target names a path in the `/app`
  bind, which the guest alone cannot follow, so the command lookup said
  "command not found in PATH". A symlink found in the PATH is now accepted and
  followed inside the sandbox, as bwrap's `execvp` does.
* `$ORIGIN` (0.4.2). A program PRoot starts itself gets `$ORIGIN` from the host
  path of the file, which the sandbox does not have: Inkscape (RPATH
  `$ORIGIN/../lib/inkscape`) failed with "libinkscape_base.so: cannot open
  shared object file". Flatpak sandboxes (those with `/app`) now start the app
  through `/usr/bin/sh -c 'exec "$0" "$@"'`; as an exec inside the sandbox it
  has the sandbox's own path.
* Large installs. A 2.6 GB install (LibreOffice) ended on "The connection is
  closed"; the guest's `flatpak` wrapper runs `install` and `update` again on a
  network error (five runs at most) and OSTree goes on where it stopped.

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
  neither. `snap install` runs the guest's `unsquashfs` (Debian squashfs-tools)
  instead of vendoring a decompressor, and installs squashfs-tools with apt
  itself when it is missing: it already runs as root and needs the network.
* Downloads are bounded by progress, not total time: a connection that sends
  nothing for 60 s is dropped and resumed with an HTTP Range (up to 8 times),
  and an interrupted download stays in the cache as `<name>_<rev>.snap.partial`
  for the next `snap install` to continue. A terminal shows MB, percent and
  speed. (The first version had a 10-minute limit on the whole download, which
  no 250 MB snap could meet on a slow link, and started over every time.)
* Aliases: the commands the store grants a snap (`gcloud`, `gsutil` and `bq`
  for google-cloud-cli) become `/snap/bin/<alias>`, read from the snap's
  snap-declaration as snapd does. An alias whose path is taken is skipped and
  reported; removing the snap removes only aliases that still run it.
* Hard links: PRoot stores each hard link of an unpacked snap as a symlink to
  the path it was made at, so the snap is unpacked straight into
  `/snap/<name>/<rev>` and never renamed afterwards. (A first version unpacked
  into `<rev>.partial` and renamed, which left every hard link dangling:
  core18's `python3.6` broke httpie.) Snaps installed that way are repaired
  once, the first time they run.
* `snapctl` is the snap binary under another name. A strict snap's sandbox gets
  it bound at `/usr/lib/snapd/snapctl`, where its base's `/usr/bin/snapctl`
  points; a classic snap finds `/usr/local/bin/snapctl`. `get`, `set`, `unset`
  keep one JSON document per snap, `/var/snap/<snap>/config.json`, shared by
  every revision, locked and written atomically (Nextcloud's apache and
  php-fpm set their defaults at the same moment). `start`, `stop` and
  `restart` work from a hook: the request goes into a spool file and
  `snap` applies it when the hook ends, as snapd does, so the sandbox never
  reaches the engine. `stop --disable` and `start --enable` decide whether a
  service starts with its snap (caddy disables its own). `is-connected`
  answers true for every plug the snap declares; there are no interface
  connections. `snap get`, `snap set` (which runs the configure hook and puts
  the old configuration back if it fails), `snap unset`, `snap restart`,
  `snap refresh` and `snap run --shell` are there too.
* Services start when the snap is installed, except a daemon with a `timer`,
  which only snapd's timer can run on schedule. Nothing restarts a service
  that exits: there is no `restart-condition` supervisor yet, so a service
  that stops (etcd without its config file) stays stopped.
* Refreshing keeps the data: `/var/snap/<snap>/<rev>` moves to the new
  revision, `current` points at it, and the old revision's `pre-refresh` and
  the new one's `post-refresh` and `configure` hooks run.
* Not available: the snapd API socket (certbot asks it for plugins and
  complains), interface connections, and confinement.
* Engine Guard refuses the real `snapd` package, as it does dockerd.
* The Snap Store backend is not free software. F-Droid will likely want the
  `NonFreeNet` anti-feature for it; Flathub's server side is free.

## Networking: one address space for containers, sandboxes and snaps

There are no network namespaces, so without care every Flatpak sandbox and
snap shares the app's loopback, and two servers on the same port collide (two
snaps on :80, or a snap on :8080 next to `docker run -p 8080:80`). Each sandbox
and each snap service therefore gets its own loopback address from the **same**
allocator that hands out user-network addresses to containers today
(`127.77.x.y`, PRoot `--net-ip`, `internal/network`). One allocator means one
address can never belong to two workloads; exhaustion is an error, never a
reuse. A service that listens on 0.0.0.0 is rewritten to its own address by
`--net-ip`, as for containers. Reaching it from the phone uses that address;
publishing to the phone's port (like `-p`) is a later, explicit option.

A **snap** has one address for all its sandboxes (`snap-addrs.json`, kept
across restarts, returned by `snap remove`). Under snapd a snap's services and
commands share the host's network, so `redis.cli` reaches the redis service on
`localhost`; with one address per snap, `localhost` in each of the snap's
sandboxes is that address. Two snaps never share one, so two services on the
same port in different snaps still do not collide. Flatpak apps keep one
address per sandbox. A snap's address is also what a browser needs:
`snap services` lists it, and a web app that checks the host name
(Nextcloud's trusted domains) has to be told it, for example
`sudo nextcloud.occ config:system:set trusted_domains 1 --value='127.77.*'`.
Ports below 1024 are served 30000 higher (80 is 30080).

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
* Stress (owner requirement, 2026-11-01): many snaps and many Flatpak apps
  installed and running at the same time next to Docker containers, including
  server snaps (cloud/self-hosted kinds, e.g. a web or database snap) that open
  ports. Check: every running workload has a distinct address
  (`/thothdock/addresses` dump vs `ss -ltn` inside each); two snaps on the same
  port both answer on their own addresses; a snap and `docker run -p` on the
  same port do not collide; a snap next to a container on a user network;
  allocator exhaustion fails the next start cleanly; addresses are released on
  stop and restart; Process Guard and Android's phantom-process cap under the
  combined load (measure where the engine is killed, never guess).
* Host suite: the shim's argv parser against bwrap's own option table, and a
  replay of real `flatpak run` command lines captured with
  `FLATPAK_BWRAP=/usr/bin/echo`.

## Phase 3 (not planned yet)

GUI apps: an X11 or Wayland server rendered to the Android app or to the
browser (Web Panel), for example with Xvfb plus a VNC or Xpra HTML5 client.
That is a separate design.

## Results (SM-A165F, Android 16, 2026-11-01)

`tests/device/app-stores-stress.sh 300`: 11 workloads at once (3 web and 2
redis containers on a user network, 4 Flatpak `python3 -m http.server`
sandboxes, the redis and mosquitto snaps from the Snap Store, one
default-network container published on 127.0.0.1:18080). Every workload had
its own loopback address and none was shared; all 7 web servers on :8080 and
the 3 redis servers on :6379 answered as themselves (separate data), the
published port answered, and all kept answering for 300 s. 42 processes at
peak (13 at rest), Process Guard "high", engine RSS 12-16 MB; after teardown
no sandbox was left, processes were back to 13 and addresses were reused.

**Known limitation.** A container on the default network shares the phone's
network, so its servers bind every address (0.0.0.0:PORT). Linux refuses that
bind when any workload with its own address already holds 127.77.x.y:PORT
("Address already in use"). Workloads with their own address (user-network
containers, Flatpak sandboxes, snaps) never conflict with each other. Giving
default-network containers pool addresses too would remove this, but changes
how they reach each other today (through localhost): an owner decision.
