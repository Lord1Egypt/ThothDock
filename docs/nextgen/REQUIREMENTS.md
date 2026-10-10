# Requirements

Each requirement has an ID that tickets (`TICKETS.md`), tests and evidence
refer to. **Status** reflects verified evidence only.

## Non-functional (the golden baseline)

| ID | Requirement | Measure | Status |
|---|---|---|---|
| NFR-01 | Idle cost stays zero: no new periodic wakeup, goroutine loop or resident process when nothing happens | `tests/perf/baseline.sh`: `idle_*.tree_wakeups` and `tree_cpu_ms` per 10 s window stay 0 in steady state | met on the host (0 / 0, also with four containers on a user network); device PENDING |
| NFR-02 | Memory: idle daemon RSS within +10% of v0.1.1 (11.7 MiB empty, 17.8 MiB with four containers on the host) | `idle_*.daemon_rss_kib` | met on the host by the complete build (+4%: 12.2 / 18.7 MiB); device PENDING |
| NFR-03 | A crash loop cannot drain the battery: restarts back off to at most one per minute | `tests/regression/restart-policies.sh` | met (host) |
| NFR-04 | Features not in use cost nothing: Compose, Web Panel, networks | process count and RSS with the feature unused | by design (ADR-0005/0006); measured per phase |
| NFR-05 | No change to the default execution path (CLI → engine → PRoot) | architecture review | met |
| NFR-06 | Nothing is exposed beyond loopback unless the owner opts in | tests: default `-p` binds 127.0.0.1; network-attached workloads never listen on a wildcard address | met for `-p` (v0.1.1); networks met on host |
| NFR-07 | No credential, session or pairing code crosses the LAN in clear text | Web Panel serves HTTPS only | PLANNED |
| NFR-08 | Existing containers and data roots keep working unchanged after an upgrade | records from v0.1.1 decode to the old behaviour; upgrade test | met (record fields are additive) |

## Functional

| ID | Requirement | Ticket |
|---|---|---|
| FR-LC-01 | Container states follow one transition table; illegal moves are refused, never silently applied | P1-01 |
| FR-EVT-01 | `GET /events` streams container, image, network and volume events in dockerd's format | P1-02 |
| FR-EVT-02 | Event filters `type`, `event`, `container`, `image`, `label`, `network`, `volume`; `since`/`until` replays | P1-02 |
| FR-EVT-03 | Memory for events is bounded; a slow consumer is disconnected, never allowed to block the engine | P1-02 |
| FR-RST-01 | Restart policies `no`, `always`, `unless-stopped`, `on-failure[:N]` with dockerd's semantics | P3-01, P3-02 |
| FR-RST-02 | A manual stop is persisted and prevents policy restarts while the daemon runs | P3-01 |
| FR-RST-03 | Exponential backoff 100 ms → 60 s, reset after a 10 s run | P3-02 |
| FR-RST-04 | Eligible containers are restored when the daemon starts | P3-03 |
| FR-RST-05 | `docker update --restart`; resource updates refused | P3-01 |
| FR-NET-01 | `docker network create/ls/inspect/rm/prune/connect/disconnect` for user-defined networks | P2-01 |
| FR-NET-02 | Each network-attached container has its own loopback address; its `localhost` is private | P2-02 |
| FR-NET-03 | Containers on a shared network resolve each other by name, alias and Compose service name | P2-03 |
| FR-NET-04 | Two containers may listen on the same port | P2-02 |
| FR-NET-05 | `-p` to a network-attached container forwards to that container's address | P2-05 |
| FR-NET-06 | `host.docker.internal` reaches the device's loopback services | P2-02 |
| FR-NET-07 | UDP within a container's own address works; cross-container UDP and UDP publishing are assessed separately | P2-06 |
| FR-CMP-01 | `docker compose config/pull/up/up -d/ps/logs/exec/stop/start/restart/down` work for supported stacks | P4-04 |
| FR-CMP-02 | Compose project, service and container labels; scoped cleanup never touches other projects | P4-03 |
| FR-CMP-03 | Unsupported Compose keys fail with an explicit error | P4-06 |
| FR-CMP-04 | Compose comes from the guest distribution (no bundled binary) | P4-01 |
| FR-CMP-05 | Tested against Debian's Compose package and the current upstream release | P4-05 |
| FR-UI-01..05 | Android Images, Volumes, Networks, Stacks screens; consistent with the CLI | P5-* |
| FR-WEB-01 | Web Panel off by default; a separate process that frees everything when stopped | P6-01, P6-06 |
| FR-WEB-02 | HTTPS with a self-signed certificate whose fingerprint the owner can check | P6-02 |
| FR-WEB-03 | One-time pairing code; revocable sessions; CSRF and Origin checks | P6-02 |
| FR-WEB-04 | Dashboard: engine, containers, images, volumes, networks, recent events | P6-03 |
| FR-WEB-05 | Start, stop, restart, remove (confirmed), logs | P6-04 |
| FR-WEB-06 | Deploy a Compose stack from YAML, validated first | P6-05 |
| FR-WEB-07 | Allow list of operations; never a raw Docker API proxy | P6-01 |
| FR-WEB-08 | Binds 127.0.0.1 unless a LAN address is chosen explicitly, with a warning | P6-02 |
| FR-SRV-01..05 | Server Mode: owner-enabled boot start, restore, battery and storage safeguards, long-run tests | P7-* |
