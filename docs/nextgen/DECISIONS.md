# Decisions

| ADR | Decision | Status |
|---|---|---|
| [0001](adr/ADR-0001-userspace-backend-stays-default.md) | The PRoot userspace backend stays the default and only execution path | ACCEPTED |
| [0002](adr/ADR-0002-loopback-addressed-networks.md) | User-defined networks give each container its own 127.77.x.y address, rewritten by PRoot `--net-ip` | ACCEPTED, host-verified; device PENDING |
| [0003](adr/ADR-0003-events-in-memory-ring.md) | Events: in-memory 1024-event ring, push subscribers, slow consumers dropped | ACCEPTED, implemented |
| [0004](adr/ADR-0004-restart-policies-docker-semantics.md) | Restart policies with dockerd's semantics, driven by process exit | ACCEPTED, implemented |
| [0005](adr/ADR-0005-web-panel-separate-process.md) | Web Panel: separate opt-in process, HTTPS, pairing, allow list | ACCEPTED, implemented |
| [0006](adr/ADR-0006-compose-from-the-distribution.md) | Compose comes from the guest distribution; ThothDock implements its API | ACCEPTED, host-verified with two Compose builds |

Smaller decisions taken during implementation:

- Panel port 7690; LAN mode binds the Wi-Fi/Ethernet address only, never `0.0.0.0` or cellular.
- `GET /events` without `since`/`until` is live only (dockerd); replaying the ring broke attached `docker compose up`.
- `docker kill` with a signal other than SIGKILL is not a manual stop (dockerd marks every kill), so `kill -s HUP` to reload a server does not disable its restart policy. Documented difference.
- A running device-network container cannot be connected to a user network; it must be stopped first (its address is applied at start).
- Fake runtime PIDs start above `pid_max`, so recovery code exercised by tests can never signal a host process.
