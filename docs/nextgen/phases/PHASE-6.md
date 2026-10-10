# Phase 6 — ThothDock Web Panel

| Section | Content |
|---|---|
| Goals | Manage the phone's containers from a desktop browser on a trusted LAN, safely, at zero cost when off |
| Out of scope | Stack deployment from YAML (P6-05), multi-user roles |
| Current state (before) | none |
| HLD | `../SYSTEM_HLD.md` §6; ADR-0005 |
| LLD | `../SYSTEM_LLD.md` §7 |
| Sequence | owner starts it on the phone → URL, fingerprint, code → browser checks fingerprint, enters code → cookie + CSRF → actions through the engine socket → events stream refreshes the page |
| Dependencies | Phase 1 (events) |
| API changes | panel HTTP surface (allow list) |
| Data model | `<root>/panel/{cert.pem,key.pem,sessions.json,pairing.json}` (0600 where secret) |
| Android | `WebPanel.java`, Containers menu; Wi-Fi/Ethernet IPv4 only; stopped with the engine; `--exit-with-parent` from a dedicated thread |
| Security | `../SECURITY_MODEL.md` N–S |
| Performance budget | separate process: nothing in the engine; +0.72 MB stripped binary |
| Test plan | panel unit tests; browser e2e 20/20 ×3 (CSP, console, 390 px) |
| Rollback | never start it |
| Definition of Done | host DONE; device PENDING |
| Exit criteria | a paired desktop browser manages a test stack ✔ (host, Chromium); inactive when disabled ✔ (listener and files gone) |
| Known limitations | self-signed TLS needs a fingerprint check; host figures limited to what an app may read |
