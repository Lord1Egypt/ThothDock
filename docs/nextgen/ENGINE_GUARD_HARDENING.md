# Engine Guard hardening, 2026-10-10

Scope: make sure normal APT work cannot replace or damage the integrated ThothDock engine or
the bundled Docker CLI. Tested only in the disposable NextGen QA guest
(`com.thothterm.debian.qa.nextgen`, Debian 13 trixie, arm64, SM-A165F). No production app, tag,
signing key or F-Droid metadata was touched. Guard package `1.0+thothdock.3`, ThothDock `5de2fef`.

## What the guest really contains (inspected, not assumed)

* `/usr/local/bin/docker` and `/usr/local/bin/thothdock` are **not guest files**. They are
  read-only PRoot binds of `libdocker.so` / `libthothdock.so` from the APK, re-created on every
  guest launch. A package that ships those paths fails (`unable to make backup link ...:
  Operation not permitted`) and the hashes stay the same.
* `docker-cli 26.1.5` (`/usr/bin/docker`) is a separate, installable client. `/usr/local/bin` comes
  first in `PATH`; the guest also exports `DOCKER_HOST`, so even a shadowing client talks to ThothDock.
* Real candidates (Debian trixie, `apt-cache policy/depends`):

| Name | Debian candidate | Notes |
|---|---|---|
| `docker`, `docker-ce-cli`, `docker-compose-plugin` | none (not in Debian) | `apt install` ends `E: ... no installation candidate / Unable to locate package` (honest, rc 100). They exist only in Docker's own repository. |
| `docker.io` | 26.1.5+dfsg1-9+deb13u1 | `Conflicts: docker-ce`; `docker-cli` has `Breaks/Replaces: docker.io` |
| `containerd` | 1.7.24~ds1-6+deb13u1 | `Breaks: docker.io (<< 1.12)` only, no conflicts |
| `runc` | 1.1.15+ds1-2+b4 | no conflicts at all |
| `containerd.io`, `docker-ce`, `moby-engine`, `docker-engine` | none in Debian | placeholders only |
| `podman-docker` | yes | `Conflicts: docker.io, docker-cli`: it plans to **remove** the `docker.io` placeholder and `docker-cli` |
| `docker-compose` (2.26.1-4) | yes | not blocked; the source-built Compose v2 plugin is untouched |

## Gaps the evidence showed, and the fixes

| # | Evidence before | Fix |
|---|---|---|
| 1 | `apt-get -y remove runc` **succeeded**. The pin only matches an installed placeholder, so apt's candidate flipped to Debian's real runc (installing it was still refused by the hook, but `podman-docker` would also have removed the `docker.io` placeholder). | The pre-install hook now refuses `**REMOVE**` of any placeholder or of `thothdock-engine-guard`, with a friendly message. |
| 2 | `dpkg -i` of Debian's `runc` / `containerd` **installed real binaries** (`/usr/bin/runc`, `/usr/bin/containerd*`), because apt's hook never runs for a direct dpkg call. `dpkg -i docker.io` was only stopped by luck (its own `Conflicts: docker-ce` with a placeholder). | The guard package `Depends` on every placeholder and `Conflicts` with every lower version of those names. dpkg now refuses **before unpacking** (`thothdock-engine-guard conflicts with runc (<< 9999:0)`), and refuses `dpkg -r/-P` of a placeholder (`dependency problems`). |
| 3 | `doctor --guard` did not look at the apt hook, its configuration or the pin. | `thothdock doctor --guard` reports all three (Warn if missing, because the app restores them; Fail if the hook is not executable, since apt would fail closed). |

Tried and rejected: `Breaks` instead of `Conflicts`. dpkg *unpacks the real package first* and only
then refuses to configure it, leaving real binaries on disk and apt wedged on a half-installed
package. `Conflicts` is checked before unpack, so it is the right relation.

Kept as is (no evidence of a gap): epoch-9999 placeholders, apt pin priority 1001, the protocol-3
hook, enforcement files written synchronously by `RootfsManager.setupEngineGuard` before a terminal
or guest process can run apt (issue #6), background registration of the packages (issue #3 parser).

## Transactions actually run (device log: `evidence/guard/device-matrix-2026-10-10.log`)

`tests/device/guard-hardening.sh`: **48 checks, 0 failed**, plus the hand-run steps below.

| Transaction | Result |
|---|---|
| `apt-get update`, `upgrade` (5 packages), `full-upgrade` | OK; placeholders and managed files unchanged |
| `apt-get install tree jq`, remove `tree` | OK |
| `apt-get install docker-cli docker-compose` | OK (CLI stays installable) |
| `apt-get install docker.io / docker-ce / containerd / containerd.io / runc / moby-engine / docker-engine` | rc 0, "already the newest version (9999:1.0+thothdock.1)": no change |
| `apt-get install docker / docker-ce-cli / docker-compose-plugin` | rc 100, `E: ... no installation candidate`: honest, not faked |
| `apt-get --allow-downgrades install docker.io=26.1.5+... / runc=... / containerd=...` | rc 100, refused: `E: Refusing to install the stock Docker Engine: docker.io(26.1.5+dfsg1-9+deb13u1)` plus the explanation |
| `apt-get remove` and `purge` of `runc`, `docker.io`, `containerd.io`, `thothdock-engine-guard` | rc 100, `E: Refusing to remove the ThothDock Docker Engine placeholder` |
| `apt-get install podman-docker` | rc 100, same refusal (it would have removed `docker.io`) |
| `dpkg -i --force-depends` of real `runc`, `containerd`, `docker.io` debs | rc 1, refused before unpacking; no engine binary on disk |
| `dpkg -r runc`, `dpkg -P containerd` | rc 1, `dependency problems` |
| `dpkg -i` of a package shipping `/usr/local/bin/docker` and `thothdock` | rc 1, `Operation not permitted`; hashes unchanged |

Afterwards: `docker version` (client 29.8.1 / server 0.1.1), `docker info`, `docker ps`,
`docker compose version`, containers / images / volumes counts all unchanged; no
`dockerd`/`containerd`/`runc` process in the guest nor anywhere on the device
(`/proc/*/comm` scan); `doctor --guard` has no WARN/FAIL.

Recovery (hand-run): the guest was first deliberately damaged with the pre-fix bypasses (real
`containerd` and `runc` installed, `runc` placeholder removed, old guard `.2`). Installing the new
APK in place and relaunching registered the placeholders and guard `.3` again by itself, removed all
real engine binaries and kept both containers running. Deleting the hook, the apt configuration
and the pin and then opening a new terminal window rewrote all three byte-for-byte (same SHA-256)
before apt could run; apt then refused the stock engine again.

## Automated tests (all green)

* `engine-guard/test.sh`: 115 checks (recorded apt records incl. new `block-remove-*`,
  `allow-guard-upgrade`; real apt-get and dpkg in a user namespace: dpkg conflicts/depends,
  apt remove/purge, ordinary install/upgrade/full-upgrade with the guard registered, migration from a
  guest that already holds a stock engine, upgrade from guard `.2`). Also reproducible under
  umask 022 and 002 (the CI check).
* `go test ./internal/guard` (doctor enforcement checks) and `./cmd/...`.
* Existing Android tests for the issue #3 parser and issue #6 synchronous enforcement
  (`EngineGuardTest`, `EngineGuardFilesTest`): `garden-common` 375 and `garden-debian` 33 unit tests pass.
* `tests/device/guard-hardening.sh` for any disposable phone guest.

## Cost

The guard adds no daemon, no timer and no polling. Enforcement is three small file writes at guest
launch; registration is one background thread at engine start that exits after checking
`dpkg/status`. The hook runs only inside apt transactions.

## What cannot be guaranteed (no false promises)

The guard stops normal APT and dpkg use and honest mistakes. It is **not** isolation against root in
the guest, which owns the rootfs:

* `dpkg --force-depends/--force-conflicts`, `dpkg -P thothdock-engine-guard` followed by installing a
  stock package, `apt -o DPkg::Pre-Install-Pkgs::=...`, deleting the pin or the hook, or unpacking an
  engine by hand (`tar`, Docker's static `docker-*.tgz`) can put stock binaries on disk. The app rewrites the
  enforcement files whenever a terminal window opens and registers the placeholders again at the next
  engine start (shown above), but it does not prevent the attempt.
* A stock `dockerd` still cannot run usefully: no namespaces, cgroups or mount rights in the
  unrooted guest. The guard keeps the broken state from being installed and from being mistaken for the engine.
* A shadowing `docker` earlier in `PATH` (for example `PATH=/usr/bin:$PATH`) selects `docker-cli`
  26.1.5 instead of the bundled 29.8.1 client. It still talks to ThothDock through `DOCKER_HOST`.
* Explicit-version attempts download the stock `.deb` (tens of MB) before the hook refuses; a pin of
  -1 would stop the download but replace the explanation with a plain "version not found", so the hook stays
  the gate.
* `apt install docker.io` prints apt's own "already the newest version (9999:1.0+thothdock.1)". That is
  true (the placeholder is installed) and carries the ThothDock version, but apt gives no hook for a
  no-op, so it cannot show the "integrated engine" notice. `apt show docker.io` and
  `thothdock doctor --guard` explain it.
* The guard covers Debian-family guests. Arch-based editions (pacman) have no dpkg hook; they are
  outside this change.

## Files changed

ThothDock `5de2fef`: `engine-guard/{build.sh,engine-guard-hook.sh,README.Debian,test.sh}`,
`engine-guard/testdata/{block-remove-placeholder,block-remove-guard,allow-guard-upgrade}.apt3`,
`internal/guard/{guard.go,guard_test.go}`, `tests/device/guard-hardening.sh`.
App: `thothdockCommit=5de2fef` (guard `1.0+thothdock.3` bundled); no Java change was needed.
