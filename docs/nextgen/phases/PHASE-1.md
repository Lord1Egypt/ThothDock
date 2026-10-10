# Phase 1 — Core state, lifecycle and reliability

| Section | Content |
|---|---|
| Goals | Explicit transitions, an event stream, recovery that survives every new state |
| Out of scope | Live restore of running containers across a daemon restart (PRoot output cannot be reattached) |
| Current state (before) | Explicit states, PID-identity recovery; `/events` 501 |
| HLD | `../SYSTEM_HLD.md` §2, §4; ADR-0003 |
| LLD | `../SYSTEM_LLD.md` §1 (transition table), §2 (event bus) |
| Sequence | create → `create`; start → `start`; stop → `kill`(signal) → `die`(exitCode) → `stop`; rm → `destroy` (matches dockerd, tested) |
| Dependencies | — |
| API changes | `GET /events`; `restarting` status; `State.Restarting` |
| Data model | `state.restarting`, `state.manuallyStopped` (additive, decode to v0.1.1 behaviour) |
| Android | none |
| Security | — |
| Performance budget | no goroutine without a subscriber; ring ≤ 1024 events; subscriber ≤ 256 queued |
| Test plan | `internal/events` ×20, engine and API tests, Compose attached mode |
| Rollback | revert 4f7612f/e3174bb; records stay readable |
| Definition of Done | DONE |
| Exit criteria | met: no functional or performance regression against Phase 0 (A/B gate) |
| Known limitations | events lost across daemon restarts (as dockerd) |
