# Flatpak deep dive, 2026-11-02

Device SM-A165F (Android 16, 8 GB RAM), ThothTerm Debian 0.5.4 with ThothDock
0.4.0, Debian flatpak 1.16.6 through the bwrap shim. 14 apps from Flathub were
installed one after the other in the production guest and each was started
once ("smoke"): a `--version` or a headless conversion, since the guest has no
display. Results below are what was observed; fixes are in ThothDock 0.4.1.

| App | Install | Smoke with 0.4.0 | Cause and fix |
|---|---|---|---|
| Neovim `io.neovim.nvim` | ok, 893 s | ok (0.12.6) | |
| Emacs `org.gnu.emacs` | ok, 801 s | ok (batch eval prints 42) | |
| KeePassXC | ok, 672 s | failed: `Failed to load --args: Bad file descriptor` | xdg-dbus-proxy; shim fixed, `keepassxc-cli 2.8.0` |
| LibreOffice | failed after 2.6 GB: `The connection is closed` | n/a | wrapper retry; second run finished in 58 s (OSTree kept the objects); smoke then hit /dev/dri, see below |
| Inkscape | ok, 878 s | failed: `bind source "/dev/dri"` | /dev binds skipped; fixed |
| GIMP | ok, 1011 s | ok (3.2.6) | |
| VLC | ok, 1517 s | ok (3.0.24) | |
| Firefox | ok, 1316 s | failed: dbus proxy | shim fixed, `Mozilla Firefox 159.0` |
| Blender | not in the aarch64 remote | n/a | no arm64 build on Flathub |
| Krita | failed after 3097 s: connection error | n/a | wrapper retry; second run 195 s |
| GNOME Calculator | failed: `Could not resolve hostname` | `/dev/dri` | see below; verified `gnome-calculator 51.1` on the fixed engine |
| Okular | same DNS failure at first | `/dev/dri` | |
| Thunderbird | no match in the aarch64 remote | n/a | no arm64 build |
| Meld | same DNS failure at first | `/dev/dri` | |

## What was fixed

1. **xdg-dbus-proxy** (`cmd/thothdock-bwrap/dbusproxy.go`). Apps with
   `[System Bus Policy]` entries make flatpak start `xdg-dbus-proxy` through
   bwrap with `--args=FD` and descriptors that cannot cross the engine socket.
   The shim now runs the proxy inside the sandbox and signals readiness itself.
   Verified on the phone with the shim alone (KeePassXC, Firefox, VLC).
2. **`/dev` binds** (`internal/sandbox/prepare.go`). A device bind such as
   `--dev-bind-try /dev/dri /dev/dri` named a path the guest does not have.
   The sandbox's `/dev` is the runtime's, so these are skipped. Engine change;
   verified in a QA APK on the phone with Calculator (fresh guest: the wrapper
   installed flatpak and dbus, added Flathub, installed the app, and it ran).
3. **Interrupted installs.** The guest `flatpak` wrapper runs `install` and
   `update` again (five runs at most) when stderr shows a network error,
   including a failed name lookup. Covered by `FlatpakRetryTest`.
4. `XDG_DATA_DIRS` now lists the Flatpak export directories, which silences the
   "directories currently searched" note after every install.

## Known limits (not fixed)

* The guest's `/etc/resolv.conf` is written from Android's active network when
  a window starts. After a network change, a long-running window keeps stale
  name servers (Okular, Meld, Calculator and Thunderbird installs failed with
  `Could not resolve hostname` until a new window was opened).
* GUI apps cannot show a window: there is no display server in the guest.
* Install time is dominated by OSTree checkout (13-25 minutes for a first app
  with a new runtime), not by download speed (about 27 MB/s).
* Resource use: 14 apps and 29 runtimes were about 13 GB in
  `~/.local/share/flatpak`.
* No arm64 build on Flathub: Blender, Thunderbird.
