# Phase 4 — Docker Compose v2

| Section | Content |
|---|---|
| Goals | The official Compose client works for multi-service stacks |
| Out of scope | build, healthchecks (P4-07), swarm, profiles needing kernel features |
| Current state (before) | Compose failed at the first network call |
| HLD | ADR-0006 |
| LLD | no Compose code in ThothDock: the API work of Phases 1–3 |
| Sequence | `up -d`: list containers/networks/volumes by project label → create network → create containers with aliases → start in dependency order; `down`: stop/remove project containers → remove project network |
| Dependencies | Phases 1–3 |
| API changes | — beyond Phases 1–3; events live-only without `since` |
| Data model | — (labels already stored) |
| Android | `apt install docker-compose` in the Debian guest (2.26.1-4); device PENDING |
| Security | `down` is label-scoped; published on loopback |
| Performance budget | zero when Compose is not running |
| Test plan | `compose.sh` 26/26 with v5.5.1, Debian 2.26.1-4 and (CI) v2.38.2 |
| Rollback | — |
| Definition of Done | host DONE; device and TON stack PENDING |
| Exit criteria | two-service stack up, communicating, down scoped ✔ (host) |
| Known limitations | see COMPATIBILITY_MATRIX `compose.*`; healthchecks refused |
