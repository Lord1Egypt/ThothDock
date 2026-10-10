# Phase 5 — Android management UI

| Section | Content |
|---|---|
| Goals | Images, Volumes, Networks and Stacks screens beside Containers |
| Out of scope | metrics from cgroups (none exist) |
| Current state | Containers screen (3 s poll while visible) and the Web Panel menu |
| HLD | screens are API clients only; event-driven refresh replaces polling |
| LLD | to write when started |
| Dependencies | Phases 1, 2, 4 (the Web Panel views are the reference) |
| API/data changes | none expected |
| Android | lazy activities; nothing runs when closed |
| Security | deletion confirmations; in-use protection from the engine |
| Performance budget | zero when closed |
| Test plan | unit tests for parsing; device UI checks under the QA package (guarded taps) |
| Rollback | — |
| Definition of Done / Exit | UI and CLI agree on every view |
| Known limitations | — |
| Status | PLANNED |
