# Phase 7 — Android Server Mode

| Section | Content |
|---|---|
| Goals | An owner-enabled mode that turns an old phone into a home server within Android's rules |
| Out of scope | persistence after force-stop; uncontrolled wake locks |
| Current state | restore at engine start exists (Phase 3); no boot start |
| HLD | `../ANDROID_LIFECYCLE.md` §Server Mode |
| LLD | boot receiver → foreground service → `ThothDock.ensureRunning()`; settings switch; battery-optimisation request with explanation |
| Dependencies | Phase 3 |
| API/data changes | none in the engine |
| Android | Android 15+ boot restrictions on FGS types (specialUse expected allowed — verify) |
| Security | Server Mode does not change exposure: panel and `-p` keep their defaults |
| Performance budget | the existing foreground service only |
| Test plan | P7-05 long runs, recorded only after they ran |
| Rollback | switch off |
| Definition of Done / Exit | demonstrated low-overhead long run within lifecycle limits |
| Known limitations | OEM battery managers may still stop the app |
| Status | PLANNED |
