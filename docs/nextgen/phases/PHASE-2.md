# Phase 2 — Networking and userspace isolation

| Section | Content |
|---|---|
| Goals | Names, own addresses, private localhost, same port twice, no LAN exposure — without root and without a resident process |
| Out of scope | Kernel network namespaces; packet filtering; cross-container UDP publishing |
| Current state (before) | Every container on the device network |
| HLD | `../SYSTEM_HLD.md` §3; ADR-0002 |
| LLD | `../SYSTEM_LLD.md` §4 (model, IPAM, hosts, publishing), §5 (PRoot rule table) |
| Sequence | create on network → allocate 127.77.x.y → write own hosts → sync peers' hosts → start with `--net-ip` → workload `bind(0.0.0.0:80)` → PRoot rewrites to `127.77.x.y:30080` → peer `connect(web:80)` resolves via hosts → PRoot shifts to 30080 |
| Dependencies | Garden patch 0009 (`--net-ip`); engine detects it from `proot --help` |
| API changes | `/networks/*`; NetworkSettings.Networks; NetworkMode for user networks |
| Data model | `networks.json`; record `networks`, `netIP` |
| Android | patch 0009 compiles for arm64 (NDK r23, API 26) and ships in the QA APK; device verification PENDING: 127/8 binds, `pidfd_getfd` under SELinux, nginx `[::]:80` |
| Security | `../SECURITY_MODEL.md` G, B, J, K, L |
| Performance budget | no new ptrace stop; no process per container or connection; idle gate in network mode |
| Test plan | probe 14/14, `net-ip-check.sh` 18/18 (local ×2 and CI), `networks.sh` 22/22 (local and CI), Compose |
| Rollback | do not create networks; the engine refuses them without `--net-ip` |
| Definition of Done | host DONE; device PENDING |
| Exit criteria | two services by alias ✔, disconnect ✔, ports cleaned ✔, LAN off by default ✔, no exposure ✔, idle cost ✔ (final host gate: 0 ms CPU and 0 wakeups per 10 s for the whole tree with four containers on a user network) on host; device and TON stack PENDING |
| Known limitations | peers seen as 127.0.0.1; no DNS server (hosts only); stopped peers stay listed; `sendmsg` destinations not rewritten; membership does not filter connections yet (P2-04b) |
