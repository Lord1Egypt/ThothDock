# Docker compatibility

ThothDock implements Docker Engine API **1.41** (minimum 1.24). Newer clients
negotiate down to it; requests for a newer version are refused with dockerd's
error.

The status labels are:

- **SUPPORTED**: works with the stock Docker CLI and was verified this round.
- **PARTIAL**: works, with the semantic difference stated.
- **UNSUPPORTED**: refused with an explanation, because it would need kernel
  features PRoot does not have.
- **PLANNED**: refused for now; it can be done in user space.
- **UNVERIFIED**: implemented, but not yet exercised end to end.

The "Verified" column says where a feature was proven: **H** is the Linux
host with the Docker CLI, **P** is the Android phone with the Docker CLI,
**T** is the automated tests.

| Docker feature | ThothDock status | Semantics | Limitation | Verified / planned phase |
|---|---|---|---|---|
| `docker version`, `info`, `/_ping` | SUPPORTED | Real values; `info` reports `CgroupDriver: none`, false for every cgroup feature, and warnings | — | H P T |
| `docker ps [-a] [--filter id/name/status/label/exited/ancestor]` | SUPPORTED | | Other filters return 400 | H P T |
| `docker images`, `image inspect`, `history`, `tag`, `rmi` | SUPPORTED | Image ID = config digest, as in Docker | `--filter`: reference, dangling, label only | H T; `images`/`inspect` also P |
| `docker pull` | SUPPORTED | OCI index / Docker manifest list, platform selection, anonymous or `X-Registry-Auth` basic credentials, bearer tokens | gzip and uncompressed layers; **zstd layers UNSUPPORTED** (no decoder in Go's standard library); foreign layers refused | H P T |
| `--platform` other than the device's | UNSUPPORTED | No emulation (QEMU is not wired in) | — | Later, optional |
| `docker create`, `start`, `run`, `run -d`, `run --rm` | SUPPORTED | Exit codes as Docker; missing or non-executable command → 127 / 126 | `argv[0]` becomes the resolved path (`sh` → `/bin/sh`) | H P T |
| `run -i` (stdin), `run -it` (TTY), resize | SUPPORTED | Real pseudo-terminal from `/dev/ptmx` | Detach keys (Ctrl-P Ctrl-Q) not implemented | H P T |
| `docker attach`, `start -a` | SUPPORTED | Hijacked stream, multiplexed or raw | — | H P T |
| `docker logs [-f] [-t] [--tail] [--since] [--until]` | SUPPORTED | json-file, rotated at 10 MiB with one old file | `--log-opt` ignored with a warning; other drivers refused | H P T |
| `docker stop`, `restart`, `kill`, `wait` | SUPPORTED | Stop signal (image `StopSignal` or SIGTERM) to the process group, then SIGKILL after `-t` | No PID namespace: a workload is not "PID 1", so it dies on SIGTERM even without a handler | H P T |
| `docker rm [-f]`, `container prune` | SUPPORTED | | `prune` filter: label only | H P T |
| `--user` | PARTIAL | uid/gid from the container's `/etc/passwd` and `/etc/group`, faked by PRoot `--change-id` | Supplementary groups are the host's; no real privilege separation | H T |
| `-e`, `--env`, `-w`, `--entrypoint`, `--name`, `--hostname`, `--label` | SUPPORTED | | `uname -n` / `hostname` show the device name (no UTS namespace); `/etc/hostname` and `$HOSTNAME` are the container's | H T (`--hostname` T only) |
| `--dns`, `--dns-search`, `--dns-option`, `--add-host` | SUPPORTED | Per-container `resolv.conf` / `hosts`, bound in by PRoot | — | T |
| `-v /host/dir:/path` | PARTIAL | Only below `serve --allow-bind` directories, canonicalised; never the data root | `:ro` refused (PRoot cannot enforce it) | T |
| Named volumes: `docker volume create/ls/inspect/rm/prune`, `-v NAME:/path`, `--mount type=volume\|bind` | SUPPORTED | Directory `volumes/<name>/_data` in the data root; created on first use; survives container removal; `rm`/`prune` refuse volumes in use (`-f` does not override); removal never follows symlinks | `:ro` and `--mount type=tmpfs` refused; no copy-up of image content into a new volume; anonymous volumes only for an empty `-v /path`, not from an image's `VOLUME`; `--volumes-from` PLANNED; no volume drivers | H P T |
| `-p [127.0.0.1:]host:container[/tcp]` TCP publishing | PARTIAL | A user-space forwarder listens on the host side and connects to `127.0.0.1:<container port>` (containers share the device network). Binds before the process starts; a collision fails `run` with a clear error; listeners close on stop, rm, daemon exit and crash. `ps` and `inspect` report the mapping | Default bind is **127.0.0.1**; other addresses need `serve --allow-publish-nonlocal`; **UDP refused**; `-P` refused; the workload's own bind address is not controlled, so a service that listens on `0.0.0.0` inside is reachable on the device network regardless of `-p` | H P T |
| `--network host` / default | PARTIAL | Every container behaves as `--network host` | No isolation | H P |
| User networks, `docker network *` | SUPPORTED (nextgen, host) | Own 127.77.x.y address per container via PRoot `--net-ip` (Garden patch 0009); names and aliases through `/etc/hosts`; see docs/nextgen/COMPATIBILITY_MATRIX.md | Not a security boundary; no DNS server; internal/IPv6/custom IPAM refused | H T (phone pending) |
| `--network none`, `container:<name>`, links | UNSUPPORTED | Needs network namespaces (`container:` planned) | — | — |
| `docker exec` (`-i`, `-t`, `-it`, `--user`, `--workdir`, `--env`, `-d`) | SUPPORTED | A second PRoot tracee on the container's root filesystem, environment and binds; exit code propagated, start failures give 126/127 like dockerd; `exec inspect` and resize work | No shared PID view: the exec process is not listed in the container's process table; `--privileged` refused | H P T |
| `--memory`, `--cpus`, `--cpu-shares`, `--pids-limit`, blkio, ulimits | UNSUPPORTED | Needs cgroups; refused, never silently ignored | — | — |
| `--privileged`, `--cap-add/--cap-drop`, `--device`, `--gpus`, `--security-opt`, `--sysctl`, `--read-only`, `--tmpfs`, `--shm-size` | UNSUPPORTED | Needs kernel privileges or mounts | — | — |
| `--restart` policies, `docker update --restart` | SUPPORTED (nextgen, host) | dockerd semantics, backoff 100 ms → 60 s, restored when the engine starts | `kill` with a signal other than SIGKILL is not a manual stop | H T (phone pending) |
| Android Containers screen (QA build) | SUPPORTED | A client of this API only: list, Start, Stop, Restart, Logs, Delete (confirmed), Shell. Keeps no state of its own, so it always agrees with `docker ps -a` | No CPU/RAM graphs (the engine has no cgroups to read); no Compose | P |
| `--init`, health checks | PLANNED | | Refused | Later |
| `pause` / `unpause`, `stats`, `top`, `update` | UNSUPPORTED / PLANNED | No freezer cgroup; stats and top can come from `/proc` | — | Later |
| `docker cp`, `export`, `diff`, `rename`, `commit` | PLANNED | | — | Later |
| `docker build` | PLANNED | | — | Later |
| `docker compose` | SUPPORTED (nextgen, host) | MVP commands with Compose v5.5.1, v2.38.2 and Debian 2.26.1-4 | build and healthcheck refused | H (phone pending) |
| `docker events` | SUPPORTED (nextgen) | filters, `since`, `until`; live only without them | history lost at daemon restart | H T |
| `system df` | PLANNED | | — | Later |
| Swarm, plugins, `docker push`, `import` | UNSUPPORTED / PLANNED | | — | — |
| Engine Guard (QA build) | SUPPORTED | `docker.io`, `docker-ce`, `docker-engine`, `moby-engine`, `containerd`, `containerd.io`, `runc` are placeholder packages at epoch 9999 that apt pins and a dpkg hook protects, so `apt full-upgrade` and `apt install docker.io` cannot replace ThothDock with a real engine. `docker-cli`, `docker-compose` and `docker-buildx` stay installable and updatable | Podman/crun are not guarded | H P T |
| Docker CLI inside the ThothTerm terminal (QA build) | SUPPORTED | Stock CLI 29.8.1 bundled as `libdocker.so`, `DOCKER_HOST=unix:///run/thothdock/thothdock.sock` set automatically | QA integration branch only; not in any released edition | Device-verified 2026-10-05 |
| Daemon restart while containers run | PARTIAL | Containers end with the daemon; on restart, stale "running" state becomes `exited (137)` with an explanatory `State.Error`, and leftovers are killed | No live restore | T |
