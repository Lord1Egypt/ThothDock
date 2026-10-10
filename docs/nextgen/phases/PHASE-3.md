# Phase 3 — Restart policies and recovery

| Section | Content |
|---|---|
| Goals | Long-running workloads that come back, with dockerd's semantics, without polling or drain |
| Out of scope | Defeating Android force-stop; boot start (Phase 7) |
| Current state (before) | only `no` accepted; everything `exited (137)` after a daemon restart |
| HLD | `../SYSTEM_HLD.md` §5; ADR-0004 |
| LLD | `../SYSTEM_LLD.md` §3 (decision, backoff, restore) |
| Sequence | exit → monitor decides → `restarting` + timer → timer → start (unless stopped/removed) ; daemon start → reconcile → restore eligible, oldest first |
| Dependencies | Phase 1 |
| API changes | `HostConfig.RestartPolicy` accepted; `POST /containers/{id}/update` (policy only) |
| Data model | `state.manuallyStopped` |
| Android | the engine restores at every start; the app starts the engine when opened; `ANDROID_LIFECYCLE.md` |
| Security | crash loops bounded (threat M) |
| Performance budget | one timer per waiting container only; ≤ 1 start/minute in a long crash loop |
| Test plan | engine restart tests (14), `restart-policies.sh` 21/21 local and CI |
| Rollback | `RELEASE_STRATEGY.md` §6 |
| Definition of Done | host DONE; device lifecycle PENDING |
| Exit criteria | `unless-stopped` behaves as documented ✔ (host); intentional stop final ✔; crash loops back off ✔ |
| Known limitations | non-SIGKILL `kill` is not a manual stop (difference from dockerd, chosen so `kill -s HUP` reloads do not disable a policy) |
