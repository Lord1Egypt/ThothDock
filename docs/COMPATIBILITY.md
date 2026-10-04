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
| Named volumes, `--mount`, `--volumes-from`, anonymous `VOLUME`s | PLANNED | Anonymous image volumes stay inside the container's filesystem (with a warning) | — | Phase 2 |
| `-p`, `-P` port publishing | PLANNED | Containers share the device network already | Will bind 127.0.0.1 by default | Phase 2 |
| `--network host` / default | PARTIAL | Every container behaves as `--network host` | No isolation | H P |
| `--network none`, user networks, `docker network *`, links | UNSUPPORTED | Needs network namespaces | — | — |
| `docker exec` | PLANNED | Same rootfs, environment and binds via a second PRoot tracee | Will not share a PID namespace | Next milestone |
| `--memory`, `--cpus`, `--cpu-shares`, `--pids-limit`, blkio, ulimits | UNSUPPORTED | Needs cgroups; refused, never silently ignored | — | — |
| `--privileged`, `--cap-add/--cap-drop`, `--device`, `--gpus`, `--security-opt`, `--sysctl`, `--read-only`, `--tmpfs`, `--shm-size` | UNSUPPORTED | Needs kernel privileges or mounts | — | — |
| `--restart` policies | PLANNED | | Only `no` accepted | Later |
| `--init`, health checks | PLANNED | | Refused | Later |
| `pause` / `unpause`, `stats`, `top`, `update` | UNSUPPORTED / PLANNED | No freezer cgroup; stats and top can come from `/proc` | — | Later |
| `docker cp`, `export`, `diff`, `rename`, `commit` | PLANNED | | — | Later |
| `docker build` | PLANNED | | — | Later |
| `docker compose` | PLANNED | Should work naturally once networks, volumes, ports and events exist | — | After single-container lifecycle is complete |
| `docker events`, `system df` | PLANNED | | — | Later |
| Swarm, plugins, `docker push`, `import` | UNSUPPORTED / PLANNED | | — | — |
| Daemon restart while containers run | PARTIAL | Containers end with the daemon; on restart, stale "running" state becomes `exited (137)` with an explanatory `State.Error`, and leftovers are killed | No live restore | T |
