# Roadmap

State on 2026-10-10. "Host" means proven on the Linux host and in CI; device
work needs the SM-A165F with ADB (wireless debugging) enabled.

| Phase | Scope | State |
|---|---|---|
| 0 | Audit, baseline, gates | host done; device baseline blocked on ADB |
| 1 | State machine, events, recovery | done |
| 2 | User networks, own addresses, names, PRoot `--net-ip` | host done; device pending |
| 3 | Restart policies, restore | host done; device lifecycle pending |
| 4 | Compose v2 | host done with three Compose builds; healthchecks next |
| 5 | Android Images/Volumes/Networks/Stacks screens | planned |
| 6 | Web Panel | host done (browser-tested); Android control implemented; device pending |
| 7 | Server Mode | planned |
| 8 | Security and compatibility hardening | in progress |

## Next, in order

1. **Device round on the QA build** (`com.thothterm.debian.qa.nextgen`, needs ADB): baseline (P0-03), `--net-ip` on Android (P2-02), lifecycle (P3-04), Compose from apt (P4-01), Web Panel from a desktop browser (P6-06/07), then the TON stack 24 h (P4-05).
2. **Health checks** (P4-07): the most common reason a real Compose file still fails.
3. **Stacks and Networks screens** (P5-03/04), event-driven instead of polled.
4. **Server Mode** (P7-01..03).
5. **Connection filtering by membership** (P2-04b) and the Phase 8 audit.
6. Release candidate per `RELEASE_STRATEGY.md`, only with the owner's approval.
