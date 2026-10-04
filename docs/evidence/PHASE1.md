# Phase 1 evidence — 2026-10-04

Every result below was observed in this round. The raw transcripts sit next
to this file.

## Environment

| | Host (development) | Phone |
|---|---|---|
| Machine | x86_64, WSL2 kernel 6.18.40.1, Ubuntu 24.04.4 | Samsung SM-A165F, Android 16 (SDK 36), kernel 6.12.38-android16, arm64, unrooted |
| ThothDock | linux/amd64, go1.27.1 | linux/arm64 static, go1.27.1 |
| PRoot | termux/proot `7266fb3e` built on the host (Garden's pin; also with Garden patches 0001–0008) | Garden's own `libproot.so` from ThothTerm Trixie 0.2.1 (`trixie-v0.2.0-71-g54a965fc`), copied to `/data/local/tmp/thothdock` |
| Docker CLI | 29.8.1 (`4a63305`), stock | 29.8.1 linux/aarch64 static (`docker-29.8.1.tgz`, sha256 `667395fb…6b07b48`; Docker publishes no checksum file for static tarballs). Only the `docker` client binary was copied; `dockerd`, `containerd` and `runc` were not |
| Transport | `unix://$HOME/.local/share/thothdock/run/thothdock.sock` (mode 0600) | `tcp://127.0.0.1:23750` via `--socket none --dev-tcp`, because adb's shell domain may not bind Unix sockets in `/data/local/tmp` (see GARDEN_RUNTIME_NOTES §6); stopped after the spike |
| API | negotiated `1.41 (downgraded from 1.56)` | same |

## Acceptance criteria

| # | Criterion | Evidence |
|---|---|---|
| 1 | Separate clean repository | `~/ThothDock`, its own git history; AndroidThothTerm untouched (`git status` clean in the worktree that was read) |
| 2 | Tests green | 68 tests passed, 0 failed, 0 skipped with `go test -race -count=1 ./...` and a real PRoot (`test-run.txt`); smoke test passed |
| 3 | CI exists | `.github/workflows/ci.yml`: gofmt, vet, race tests with a real PRoot, linux/amd64 and linux/arm64 builds, Docker CLI smoke test |
| 4 | Unix socket | `srw------- … thothdock.sock` (`host-evidence.txt`) |
| 5–6 | Stock Docker CLI: `version`, `info`, `ps`, `images` | `host-evidence.txt`, `phone-evidence.txt` |
| 7–9 | No dockerd / containerd / runc as engine | `Server: ThothDock …` answers every command. Host: `pgrep -x dockerd\|containerd\|runc\|containerd-shim-runc-v2` finds nothing in the environment. Phone: 0 such processes, and the process tree shows `thothdock → libproot.so → sh → sleep` |
| 10 | No systemd | `thothdock serve` is an ordinary foreground process |
| 11 | Runtime interface | `internal/runtime.Runtime`, `PRootRuntime`, `FakeRuntime` |
| 12 | PRoot process launch | Runtime tests against a real PRoot (stdout, stderr, exit 3, SIGTERM→143, SIGINT→130, kill→137 with no surviving child, stdin, TTY size); and every container run below |
| 13 | Limitations documented | `COMPATIBILITY.md`, `SECURITY_MODEL.md`, `docker info` warnings, 501 responses with reasons |
| 14 | No Garden code, release, tag or F-Droid change | Garden was read with `git show b7f4936:<path>` only; the phone's apps were not installed, changed or opened, and their files were only copied from |
| 15 | No signing material touched | None used or read |

## Stretch goal: real image lifecycle (achieved on both host and phone)

Phone, Docker CLI → ThothDock → Garden PRoot (transcribed from this round's
session output; `phone-evidence.txt` and `phone-independence.txt` are raw
captures):

```
$ docker pull alpine:3.20
3.20: Pulling from library/alpine
3f26bc2dec0b: Pulling fs layer
3f26bc2dec0b: Verifying Checksum
3f26bc2dec0b: Download complete
3f26bc2dec0b: Extracting
3f26bc2dec0b: Pull complete
Digest: sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
Status: Downloaded newer image for alpine:3.20
$ docker image inspect alpine:3.20 --format "{{.Id}} {{.Os}}/{{.Architecture}}"
sha256:ab3fe4defd29ba6231229a4d41440ac8bde8218e85870e53876277faa24b35c4 linux/arm64
$ docker run --rm alpine:3.20 echo hello from ThothDock on Android
hello from ThothDock on Android
$ docker run --rm alpine:3.20 uname -m
aarch64
$ docker run --name pkgtest alpine:3.20 apk add --no-cache curl
… (10/10) Installing curl (8.14.1-r2) … OK: 15 MiB in 24 packages
$ docker run … sh -c "apk add -q --no-cache curl && curl … https://example.com"
HTTP 200 from 172.66.147.243
$ docker run --name apttest debian:trixie-slim sh -c "apt-get update -q && apt-get install -y -q --no-install-recommends curl ca-certificates …"
Get:4 http://deb.debian.org/debian trixie/main arm64 Packages [9614 kB] … Fetched 10.1 MB in 3s
install-exit=0
curl 8.14.1 (aarch64-unknown-linux-gnu) …
HTTP 200
$ docker run -it --rm debian:trixie-slim bash        (adb shell -tt)
root@localhost:/# echo inside-$((6*7)); cat /etc/debian_version; stty size; exit 5
inside-42
13.7
25 90
it-exit=5
```

The same index digest `d9e853e8…` resolved to the arm64 config `ab3fe4de…` on
the phone and the amd64 config `bf8527eb…` on the host: platform selection
from the manifest list.

Every blob digest was verified: blobs enter the store only through
`Blobs.Ingest`, which checks size and sha256, and are re-hashed again while
applied (`image.applyVerified`). The negative cases (corrupt blob, short
blob, wrong diff ID, malicious layer, wrong platform) are tests in
`internal/image/image_test.go`.

## Bugs found and fixed during this round

Each was found by running the stock CLI or the phone, not by inspection:

- An attach without stdin ended at the CLI's immediate half-close, so stdout
  was lost. A half-close no longer ends the stream.
- `docker run --rm` with a missing command hung. A failed start now releases
  attach clients and removes `--rm` containers, as dockerd does.
- A data root whose socket path is over 107 bytes failed with `invalid
  argument`. It now gets a clear error.
- On Android, TLS trusted no CA. ThothDock now uses Android's system trust
  store.
- `stop -t` with a negative timeout killed immediately. It now waits
  indefinitely, as Docker does.
- PRoot version banners were parsed wrongly. Upstream and Garden formats are
  now both read.
