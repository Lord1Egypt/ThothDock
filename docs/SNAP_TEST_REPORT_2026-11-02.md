# Snap test campaign, 2026-11-02 (SM-A165F, Android 16, 8 GB)

Why: Nextcloud's snap failed to install cleanly ("snapctl: not found"), so 29
snaps of different kinds were installed and run to find the other things that
did not work. Two rounds: ThothDock 0.3.1 as released (round 1, 20 snaps), then
the build with the fixes below (round 2, 18 snaps; 9 were also in round 1).

## What was found and fixed

| # | Found with | Cause | Fix |
|---|------------|-------|-----|
| 1 | nextcloud | no `snapctl`: the configure hook failed, services never read their ports | `snapctl` get/set/unset/start/stop/restart/services/is-connected; `snap get/set/unset/restart/refresh` |
| 2 | httpie (core18) | PRoot stores a snap's hard links as symlinks to the path they were made at; unpacking into `<rev>.partial` and renaming left every one dangling (`python3.6`, `perl`, ... in core18) | unpack into the final directory; installed snaps are repaired once (447 links in core18) |
| 3 | redis | each sandbox had its own loopback address, so `redis.cli` could not reach the `redis` service on localhost | one persistent address per snap, shared by its services, hooks and commands |
| 4 | caddy | `snapctl stop --disable` was ignored, so the service started anyway | disabled services are remembered; `snapctl services` for hooks |
| 5 | caddy (QA build) | the engine builds a sandbox from outside the guest and does not see the app-level bind of the snap command: `snapctl` was an empty placeholder (Permission denied) | `snapctl` is bound from a real copy in `/var/lib/thothdock-snaps/bin` |
| 6 | nextcloud | services ended with the app and nothing restarted them | `snap autostart`, run by a new window; services also start at install |
| 7 | all | no way to look inside a sandbox | `snap run --shell` |

## Results

Installed and ran (smoke command succeeded): jq, htop, btop, yq, hugo,
shellcheck, yt-dlp, go, cmake, redis (client reaches server), mosquitto
(publish reaches subscriber), httpie, etcdctl, syncthing, rclone, restic,
kubectl, helm, terraform, ruby, node 24, nmap, adguard-home (service),
nextcloud 34.0.5 (8 services from `snap install nextcloud` alone, HTTP 200,
`manual-install` works, login page in the phone's browser), hello-world.

Refused correctly, with the store's own channel list: ripgrep, ffmpeg,
prometheus (no arm64 on latest/stable), node (use `--channel=24/stable`).

Data kept and services restarted by a refresh (simulated from an older
revision): marker file carried over, `current` moved, old revision removed,
mosquitto answered. Remove and reinstall: caddy stays disabled, redis gets its
address back.

## Not working, and why

* certbot: asks snapd's socket (`/run/snapd.socket`) for plugins and stops.
  It needs a snapd REST subset, which does not exist yet.
* aws-cli (classic): uses the guest's `python3`, which Debian's minimal image
  does not have; `sudo apt install python3` fixes it.
* etcd: stops at once without `/var/snap/etcd/common/etcd.conf.yml`, as it does
  under snapd; nothing restarts a service that exits (no `restart-condition`
  supervisor).
* Services with a `timer` (logrotate, certbot.renew) are not scheduled.
* Web apps that check the host name must be told the snap's address:
  Nextcloud needs `occ config:system:set trusted_domains 1 --value='127.77.*'`.

## Resources (14 snap services running, QA build)

* Visible processes: 43, 926 MB resident in total. One PRoot supervisor
  (about 37 MB) per running sandbox; `mysqld` 216 MB is the largest service.
* Disk: 3.9 GB under `/snap` for 29 snaps and 6 bases; 615 MB `/var/snap`.
* Install times: 4 to 37 s for most snaps over Wi-Fi; Nextcloud (366 MB) 86 s
  including the start of its 8 services.

## Not measured

Confinement is not enforced (documented). Behaviour with the guest killed in
the middle of an install was not tested beyond the resumable download.
