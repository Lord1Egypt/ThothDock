# Phase 8 — Security, hardening and compatibility

| Section | Content |
|---|---|
| Goals | Audit every surface added in Phases 1–7; specification-based compatibility tests |
| Out of scope | claiming parity from a subset |
| Current state | v0.1.1 hostile-input and extraction tests; nextgen security model written; hosts injection and panel rules tested |
| HLD/LLD | `../SECURITY_MODEL.md` |
| Dependencies | all phases |
| API/data changes | — |
| Android | device hostile-API script (`tests/device/hostile.sh`) to extend with networks and panel |
| Security | cooperative vs malicious workloads stated per threat |
| Performance budget | — |
| Test plan | fuzz new endpoints (filters, network names, event times), differential runs against a real dockerd with the owner's permission |
| Rollback | — |
| Definition of Done / Exit | every threat row has a test or an accepted-risk statement |
| Known limitations | no kernel isolation on stock Android |
| Status | IN_PROGRESS (P8-01) |
