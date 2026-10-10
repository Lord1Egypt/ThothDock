# ADR-0006 — Compose is the official Compose v2 client from the guest distribution; ThothDock implements the API it calls

- Status: ACCEPTED (2026-10-10)
- Requirements: FR-CMP-01..05, NFR-04
- Tickets: P4-01..P4-06

## Context

A Docker Compose binary is large: the upstream v5.5.1 plugin on the
development host is 32,333,754 bytes (amd64, stripped, measured 2026-10-10),
twice the size of the whole ThothTerm • ThothDock 0.3.1 APK (15,995,639
bytes). Debian 13 ships a `docker-compose` package (the Go Compose v2 CLI,
built from source by Debian; the version is recorded in the P4-01 evidence)
as a Docker CLI plugin, and Engine Guard already leaves `docker-compose`,
`docker-cli` and `docker-buildx` installable.

## Decision

- Do not bundle Compose. `apt install docker-compose` in the guest provides
  `docker compose` (plugin) and `docker-compose`. Both were built from source
  by Debian and are signed by its archive keys, which meets "no unverified
  downloaded executable".
- ThothDock implements the Engine API that Compose calls (networks, aliases,
  label filters, events) and is tested against two Compose builds: the
  Debian 2.26.x package and the current upstream release.
- No Compose file parser exists in ThothDock. The Web Panel (P6-05) deploys
  stacks by running the same Compose client in the guest, never by
  interpreting YAML itself.

## Consequences

- Compose features depend on the Engine API only, so the same work serves
  every Compose-compatible client.
- `docker compose build` needs BuildKit or the classic builder, which
  ThothDock does not have yet. It fails with an explicit error.
