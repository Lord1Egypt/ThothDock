# ADR-0004 — Restart policies follow dockerd's semantics, driven by process exit

- Status: ACCEPTED (2026-10-10)
- Requirements: FR-RST-01..05, NFR-03
- Tickets: P3-01..P3-04

## Decision

- Policies: `no`, `always`, `unless-stopped`, `on-failure[:N]`, persisted in
  the container record (`HostConfig.RestartPolicy`).
- `ManuallyStopped` is persisted separately. `docker stop`, `kill` from the
  API and the Android Stop button set it; `start` and `restart` clear it.
- The monitor that already waits for each container's process decides on
  exit. There is no polling and no extra goroutine per container.
- Backoff as in dockerd: 100 ms, doubling, capped at 1 minute; it resets when
  a run lasted at least 10 s. A container waiting to restart reports
  `restarting` with `Restarting: true`.
- When the daemon starts (the app was relaunched or Android restarted it), it
  restores containers whose policy is `always`, or `unless-stopped` and not
  manually stopped, or `on-failure` with the retry budget left, and that were
  running when the previous daemon ended. A process killed with the daemon
  counts as a failure (exit 137), as in dockerd.
- `docker stop` on an `always` container keeps it stopped until the daemon
  restarts. That is dockerd's behaviour, and `unless-stopped` is the policy
  that stays stopped across restarts.

## Android consequences

Android may kill the app at any time (low memory, force-stop, uninstall).
Nothing restarts containers until the engine starts again. That happens when
the user opens the app or, with Server Mode (P7), when Android starts the
foreground service at boot. Restart policies do not and cannot defeat
force-stop (`ANDROID_LIFECYCLE.md`).
