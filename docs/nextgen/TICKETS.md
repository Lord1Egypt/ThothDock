# Tickets

Status values: PLANNED, IN_PROGRESS, IMPLEMENTED, TESTING, BLOCKED, DONE.
**DONE** means the ticket's tests and evidence exist. Device rows that have
not run are PENDING, and a ticket with a PENDING device row is at most
TESTING. Field order follows the programme's ticket format; "—" means
nothing applies.

Evidence paths are relative to `docs/nextgen/evidence/` unless they name a
script. Commit references are ThothDock `feature/nextgen` unless marked
"Garden" (AndroidThothTerm `feature/thothdock-nextgen`).

---

## Phase 0 — Baseline, audit and performance protection

### P0-01 — Source and architecture audit
- **Purpose:** know what exists before changing it.
- **Existing behaviour:** v0.1.1 docs describe modules; no ownership map.
- **Expected behaviour:** every subsystem mapped with verified capabilities and limits.
- **Dependencies:** — · **Affected modules:** docs only
- **HLD/LLD changes:** `ARCHITECTURE.md` (current state).
- **API/data changes:** —
- **Implementation tasks:** read engine, api, runtime, portmap, Android supervisor; run the suites.
- **Unit tests:** existing suite, 0 failures. · **Integration tests:** smoke, lifecycle.
- **Android device tests:** — · **Security checks:** — · **Performance checks:** —
- **Definition of Done:** map written; all existing tests pass. · **Rollback:** —
- **Evidence:** `ARCHITECTURE.md`; commit b5a542b. · **Status:** DONE

### P0-02 — Golden workload fixture
- **Purpose:** a repeatable version of the four-container phone scenario.
- **Existing behaviour:** owner observation only.
- **Expected behaviour:** a deterministic local stack for CI, plus the real TON API + Explorer stack as an owner-assisted device test.
- **Dependencies:** P2, P4 · **Affected modules:** tests
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** `tests/fixtures/compose/two-service`; a Compose file for the TON stack (needs the owner's image ports and env).
- **Unit tests:** — · **Integration tests:** `compose.sh` (host 26/26 with two Compose builds).
- **Android device tests:** PENDING (TON stack, 4 containers, 24 h).
- **Security checks:** published on loopback only. · **Performance checks:** idle profile during the run.
- **Definition of Done:** both fixtures run with timestamped evidence. · **Rollback:** —
- **Evidence:** `compose.sh` output in phase notes. · **Status:** IN_PROGRESS (deterministic part done; TON stack PENDING)

### P0-03 — Resource baseline
- **Purpose:** numbers every later change is compared with.
- **Existing behaviour:** none measured.
- **Expected behaviour:** idle CPU, wakeups, RSS, latencies, throughput, repeated.
- **Dependencies:** — · **Affected modules:** `tests/perf`
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** `baseline.sh` (all threads and the whole tree counted; monotonic clock), `compare.py`.
- **Unit tests:** — · **Integration tests:** three v0.1.1 host runs.
- **Android device tests:** BLOCKED — ADB was not reachable (the phone answers ping; wireless debugging is off). Plan: read-only `/proc` sampling of the live daemon, then the harness under the QA package.
- **Security checks:** — · **Performance checks:** this is the baseline.
- **Definition of Done:** host and device baselines recorded. · **Rollback:** —
- **Evidence:** `phase0/baseline-v0.1.1-run{1,2,3}.txt`, `PERFORMANCE_BASELINE.md`. · **Status:** TESTING (host DONE, device BLOCKED)

### P0-04 — Performance regression gates
- **Purpose:** keep the golden idle profile.
- **Existing behaviour:** none.
- **Expected behaviour:** explicit budgets, interleaved A/B comparison.
- **Dependencies:** P0-03 · **Affected modules:** docs, tests/perf
- **HLD/LLD changes:** `PERFORMANCE_BUDGET.md` · **API/data changes:** —
- **Implementation tasks:** budgets, compare tool, gate runs after each phase.
- **Unit tests:** — · **Integration tests:** gate runs (phase1-3, final).
- **Android device tests:** PENDING. · **Security checks:** — · **Performance checks:** the gates themselves.
- **Definition of Done:** gates defined and passed on the host. · **Rollback:** —
- **Evidence:** `phase1-3/`, `final/`. · **Status:** DONE (host); device gate is part of P0-03

### P0-05 — CI for the next-generation runtime
- **Purpose:** networks, Compose and policies tested on every push.
- **Existing behaviour:** CI used termux PRoot without --net-ip.
- **Expected behaviour:** a job building Garden PRoot 0001-0009 and running the regressions.
- **Dependencies:** P2-02 · **Affected modules:** `.github/workflows/ci.yml`
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** `nextgen` job; matrix render check in lint.
- **Unit tests:** — · **Integration tests:** the job. · **Android device tests:** —
- **Security checks:** pinned Garden commit. · **Performance checks:** —
- **Definition of Done:** job green. · **Rollback:** drop the job.
- **Evidence:** GitHub Actions run 38053789668 (`nextgen` job: 87 checks passed, Compose v2.38.2). · **Status:** DONE

---

## Phase 1 — Core state, lifecycle and reliability

### P1-01 — Explicit container state machine
- **Purpose:** no silent state corruption.
- **Existing behaviour:** explicit states, but any code path could write any state.
- **Expected behaviour:** one transition table; illegal moves refused with 409.
- **Dependencies:** — · **Affected modules:** engine (`state.go`, `lifecycle.go`)
- **HLD/LLD changes:** HLD §2, LLD §1 · **API/data changes:** new `restarting` state.
- **Implementation tasks:** `transition()`, used by start, monitor, stop, remove; recovery exempt.
- **Unit tests:** `TestTransitionGuardRefusesIllegalMoves`, every lifecycle test.
- **Integration tests:** lifecycle, restart-policies. · **Android device tests:** — (pure logic)
- **Security checks:** — · **Performance checks:** none (map lookup).
- **Definition of Done:** tests pass, race-clean ×5. · **Rollback:** revert 4f7612f.
- **Evidence:** commit 4f7612f. · **Status:** DONE

### P1-02 — Event-driven engine state (/events)
- **Purpose:** Compose, UIs and `docker events` without polling.
- **Existing behaviour:** `/events` returned 501.
- **Expected behaviour:** dockerd's event format, filters, since/until; bounded memory; live only without since.
- **Dependencies:** — · **Affected modules:** `internal/events`, engine, api
- **HLD/LLD changes:** ADR-0003, LLD §2 · **API/data changes:** `GET /events`.
- **Implementation tasks:** bus, publishers (container, image, volume, network), handler.
- **Unit tests:** `internal/events` (×20 stable), `internal/api/events_test.go`.
- **Integration tests:** `restart-policies.sh` (docker events), attached `docker compose up`.
- **Android device tests:** — · **Security checks:** — · **Performance checks:** idle zero without subscribers (gate).
- **Definition of Done:** tests pass; Compose attached mode correct. · **Rollback:** revert.
- **Evidence:** 4f7612f, e3174bb (live-only fix). · **Status:** DONE

### P1-03 — Runtime and process supervision audit
- **Purpose:** keep PID-identity and process-group protections.
- **Existing behaviour:** PID + start time checks, pidfd kills, group signals.
- **Expected behaviour:** unchanged; the new code uses the same primitives.
- **Dependencies:** — · **Affected modules:** runtime, engine, tests
- **HLD/LLD changes:** — · **API/data changes:** `Spec.NetIP`.
- **Implementation tasks:** audit; fake runtime PIDs moved above `pid_max` (a test could otherwise SIGKILL a host process group).
- **Unit tests:** runtime tests with real PRoot. · **Integration tests:** SIGKILL cycles in three regressions, orphan checks.
- **Android device tests:** PENDING (QA lifecycle matrix). · **Security checks:** no signal to an unverified PID.
- **Performance checks:** — · **Definition of Done:** no orphans in any regression. · **Rollback:** —
- **Evidence:** regression outputs. · **Status:** TESTING (device PENDING)

### P1-04 — Structured failure recovery
- **Purpose:** idempotent recovery that never restarts a manually stopped container.
- **Existing behaviour:** reconcile, interrupted create/remove/pull.
- **Expected behaviour:** plus `restarting` records, missing networks, address conflicts.
- **Dependencies:** P1-01, P3 · **Affected modules:** engine
- **HLD/LLD changes:** `FAILURE_RECOVERY.md` · **API/data changes:** —
- **Implementation tasks:** reconcile `restarting`; `loadNetworking` (writes only on change).
- **Unit tests:** `TestStaleRunningStateIsReconciled`, `TestRestoreAtDaemonStart`, `TestAddressesSurviveADaemonRestart`.
- **Integration tests:** SIGKILL/SIGTERM cycles. · **Android device tests:** PENDING (force-stop + relaunch).
- **Security checks:** — · **Performance checks:** no write at start unless needed.
- **Definition of Done:** tests pass. · **Rollback:** revert.
- **Evidence:** 4f7612f. · **Status:** DONE (host); device row in P3-04

---

## Phase 2 — Networking and userspace isolation

### P2-01 — Network abstraction
- **Purpose:** `docker network *` and Compose networks.
- **Existing behaviour:** 501.
- **Expected behaviour:** create/ls/inspect/rm/prune/connect/disconnect; built-ins host, bridge, none.
- **Dependencies:** P2-02 for addresses · **Affected modules:** `internal/network`, engine, api
- **HLD/LLD changes:** ADR-0002, LLD §4 · **API/data changes:** `/networks/*`, `networks.json`, record `networks`/`netIP`.
- **Implementation tasks:** store, pool, endpoints, API, refusals (internal, IPv6, IPAM, drivers).
- **Unit tests:** `internal/network`, `networks_test.go`. · **Integration tests:** `networks.sh` 22/22, `compose.sh`.
- **Android device tests:** PENDING. · **Security checks:** names validated.
- **Performance checks:** none idle. · **Definition of Done:** host tests pass. · **Rollback:** revert; see RELEASE_STRATEGY §6.
- **Evidence:** 4f7612f. · **Status:** TESTING (host DONE, device PENDING)

### P2-02 — Container-to-container TCP (PRoot --net-ip)
- **Purpose:** own address per container, private localhost, same port twice.
- **Existing behaviour:** shared device network.
- **Expected behaviour:** LLD §5 rules.
- **Dependencies:** Garden patch 0009 · **Affected modules:** PRoot (Garden), runtime
- **HLD/LLD changes:** ADR-0002, LLD §5 · **API/data changes:** PRoot option `--net-ip`.
- **Implementation tasks:** extension, option, host check, static probe, NDK build.
- **Unit tests:** probe 14/14. · **Integration tests:** `net-ip-check.sh` 18/18 (×2), `networks.sh`.
- **Android device tests:** PENDING — must confirm 127/8 binding, `pidfd_getfd` under SELinux, nginx `[::]:80`.
- **Security checks:** app buffer never written (tested). · **Performance checks:** no new ptrace stop (by construction); idle gate with `PERFORMANCE_BUDGET` network mode.
- **Definition of Done:** device rows pass. · **Rollback:** the engine refuses networks when PRoot lacks the option.
- **Evidence:** Garden 428a939, c149f12. · **Status:** TESTING

### P2-03 — Service-name discovery
- **Purpose:** `http://api:3000` from a peer.
- **Existing behaviour:** none.
- **Expected behaviour:** names, aliases, Compose service names, `host.docker.internal`.
- **Dependencies:** P2-01 · **Affected modules:** engine (`hostsFor`, `syncHosts`)
- **HLD/LLD changes:** LLD §4 hosts file · **API/data changes:** —
- **Implementation tasks:** per-container hosts files, serialised rewrites.
- **Unit tests:** `TestNetworkMembersGetAddressesAndNames`, `TestConnectDisconnect`, `TestExtraHostsHostGatewayOnANetwork`.
- **Integration tests:** `networks.sh`, `compose.sh`. · **Android device tests:** PENDING.
- **Security checks:** injection-proof names; first-wins. · **Performance checks:** rewrites only on membership changes.
- **Definition of Done:** tests pass. · **Rollback:** —
- **Evidence:** 4f7612f. · **Status:** TESTING (device PENDING)

### P2-04 — Membership and isolation policy
- **Purpose:** decide who can talk to whom.
- **Existing behaviour:** —
- **Expected behaviour:** names follow membership (done). Connection filtering is P2-04b.
- **Dependencies:** P2-03 · **Affected modules:** engine; PRoot for P2-04b
- **HLD/LLD changes:** SECURITY_MODEL threat J · **API/data changes:** —
- **Implementation tasks:** P2-04b: a shared read-only membership map consulted on connect.
- **Unit tests:** name visibility tests. · **Integration tests:** `networks.sh` (other network does not resolve).
- **Android device tests:** — · **Security checks:** documented as not a boundary.
- **Performance checks:** P2-04b must stay a memory lookup in an existing stop.
- **Definition of Done:** P2-04b implemented and tested. · **Rollback:** —
- **Evidence:** — · **Status:** IMPLEMENTED (names); P2-04b PLANNED

### P2-05 — LAN publishing
- **Purpose:** no accidental exposure.
- **Existing behaviour:** `-p` binds 127.0.0.1; non-loopback needs `--allow-publish-nonlocal`.
- **Expected behaviour:** unchanged; addressed containers' workloads are not on wildcard addresses.
- **Dependencies:** P2-02 · **Affected modules:** portmap, engine
- **HLD/LLD changes:** LLD §4 publishing · **API/data changes:** `portmap.Binding.Target`.
- **Implementation tasks:** forward to the container address with the low-port shift.
- **Unit tests:** `TestPublishedPortOfAnAddressedContainerTargetsItsAddress`, portmap tests.
- **Integration tests:** `networks.sh` (no new wildcard listener), smoke (LAN refused).
- **Android device tests:** PENDING. · **Security checks:** the Engine API is never on TCP.
- **Performance checks:** — · **Definition of Done:** tests pass. · **Rollback:** —
- **Evidence:** 4f7612f. · **Status:** TESTING (device PENDING)

### P2-06 — UDP assessment
- **Purpose:** decide UDP honestly.
- **Existing behaviour:** UDP publishing refused.
- **Expected behaviour:** UDP within a container's own address works (sendto rewrite, probe-tested); UDP publishing stays refused until a forwarder exists; `sendmsg` destinations are not rewritten.
- **Dependencies:** P2-02 · **Affected modules:** portmap (future)
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** design a per-binding UDP relay with idle timeouts.
- **Unit tests:** probe UDP case. · **Integration tests:** — · **Android device tests:** —
- **Security checks:** — · **Performance checks:** relay must not poll.
- **Definition of Done:** relay implemented and tested, or the refusal kept and documented. · **Rollback:** —
- **Evidence:** probe output. · **Status:** IN_PROGRESS

---

## Phase 3 — Restart policies and recovery

### P3-01 — Restart policy state
- **Purpose:** policies and manual stops persisted separately.
- **Existing behaviour:** only `no` accepted.
- **Expected behaviour:** four policies, `manuallyStopped`, dockerd validation, `docker update --restart`.
- **Dependencies:** P1-01 · **Affected modules:** engine, api
- **HLD/LLD changes:** ADR-0004, LLD §3 · **API/data changes:** record `state.manuallyStopped`, `/containers/{id}/update`.
- **Implementation tasks:** validation, update endpoint, stop/kill/rm semantics.
- **Unit tests:** `TestRestartPolicyValidation`, `TestKillSIGKILLIsAManualStop…`, API update test.
- **Integration tests:** `restart-policies.sh` 21/21. · **Android device tests:** — 
- **Security checks:** — · **Performance checks:** —
- **Definition of Done:** tests pass. · **Rollback:** RELEASE_STRATEGY §6.
- **Evidence:** 4f7612f. · **Status:** DONE

### P3-02 — Restart supervisor
- **Purpose:** restart without polling or battery drain.
- **Existing behaviour:** —
- **Expected behaviour:** decision at exit, backoff 100 ms → 60 s, timer only while waiting.
- **Dependencies:** P3-01 · **Affected modules:** engine
- **HLD/LLD changes:** HLD §5 · **API/data changes:** `restarting` state, `Restarting` in inspect.
- **Implementation tasks:** `scheduleRestartLocked`, `policyRestart`, cancel paths.
- **Unit tests:** `TestOnFailure…`, `TestAlways…`, `TestRestartingContainerStopKillAndRemove`, `TestNextRestartDelay`.
- **Integration tests:** crash-loop backoff check (5 restarts in 4 s). · **Android device tests:** PENDING (crash loop on battery).
- **Security checks:** — · **Performance checks:** idle gate; crash-loop CPU bounded by backoff.
- **Definition of Done:** tests pass. · **Rollback:** revert.
- **Evidence:** 4f7612f. · **Status:** DONE (host)

### P3-03 — Recovery after daemon restart
- **Purpose:** workloads come back when the app comes back.
- **Existing behaviour:** everything `exited (137)` after a restart.
- **Expected behaviour:** dockerd's restore rules; shutdown is not a manual stop.
- **Dependencies:** P3-01 · **Affected modules:** engine, serve
- **HLD/LLD changes:** LLD §3 restore · **API/data changes:** —
- **Implementation tasks:** `RestoreRestartPolicies` after the socket listens.
- **Unit tests:** `TestRestoreAtDaemonStart` (9 cases), `TestShutdownIsNotAManualStop`.
- **Integration tests:** SIGKILL and SIGTERM cycles in three regressions. · **Android device tests:** PENDING.
- **Security checks:** — · **Performance checks:** restores run once, oldest first.
- **Definition of Done:** host tests pass. · **Rollback:** —
- **Evidence:** 4f7612f. · **Status:** DONE (host); device row in P3-04

### P3-05 — Phantom-process limit (new, found on the device)
- **Purpose:** keep long-running containers alive under Android's device-wide child-process cap.
- **Existing behaviour:** Android killed the production engine and its 4 containers when a second engine's processes pushed the total over the cap (log line in `ANDROID_LIFECYCLE.md`).
- **Expected behaviour:** restart policies restore them; the panel and Containers screen show the process count against the cap; a one-time owner-run command is documented to raise the cap.
- **Dependencies:** P3-03 · **Affected modules:** Android (count, warning), docs
- **Implementation tasks:** read the process count of the app tree; warn above ~24; document the adb setting.
- **Unit tests:** parser for the count. · **Android device tests:** with the cap raised, run production + QA together and confirm nothing is killed.
- **Security checks:** none (reading our own process tree). · **Performance checks:** read only when the screen is open.
- **Definition of Done:** warning shown; device test passes. · **Rollback:** —
- **Evidence:** `evidence/device/` log excerpt. · **Status:** PLANNED

### P3-04 — Android lifecycle integration
- **Purpose:** honest semantics on Android.
- **Existing behaviour:** lifecycle matrix measured for v0.1.1.
- **Expected behaviour:** `ANDROID_LIFECYCLE.md`.
- **Dependencies:** P3-03, device · **Affected modules:** Android (no code change needed for restore)
- **HLD/LLD changes:** `ANDROID_LIFECYCLE.md` · **API/data changes:** —
- **Implementation tasks:** device round with the QA package: screen off, background, Exit, force-stop + relaunch, crash loop.
- **Unit tests:** — · **Integration tests:** — · **Android device tests:** PENDING (all rows).
- **Security checks:** no wake locks added. · **Performance checks:** device idle gate.
- **Definition of Done:** every row of ANDROID_LIFECYCLE measured. · **Rollback:** —
- **Evidence:** — · **Status:** BLOCKED (device access)

---

## Phase 4 — Docker Compose v2

### P4-01 — Official Compose integration
- **Purpose:** the real Compose client, no reimplementation.
- **Existing behaviour:** —
- **Expected behaviour:** `apt install docker-compose` in the guest (Debian 13: 2.26.1-4, built from source by Debian, archive-signed); nothing bundled (ADR-0006).
- **Dependencies:** Engine Guard leaves `docker-compose` installable · **Affected modules:** docs
- **HLD/LLD changes:** ADR-0006 · **API/data changes:** —
- **Implementation tasks:** verify the package; document.
- **Unit tests:** — · **Integration tests:** fetched with apt inside a debian:trixie container on ThothDock; `compose.sh` 26/26 with it.
- **Android device tests:** PENDING (`apt install docker-compose` in the guest, `docker compose version`).
- **Security checks:** Debian archive signature (apt). · **Performance checks:** zero cost when unused.
- **Definition of Done:** device install works. · **Rollback:** —
- **Evidence:** deb sha256 47681197cb2b1ab88206b42a1a92fc75f50d9530a6322d03494a473eed0408ea. · **Status:** TESTING (device PENDING)

### P4-02 — Engine API gaps for Compose
- **Purpose:** what Compose calls must work.
- **Existing behaviour:** networks, events, labels on networks missing.
- **Expected behaviour:** networks, aliases, label filters, events (live-only without since), update.
- **Dependencies:** P1-02, P2-01 · **Affected modules:** api, engine
- **HLD/LLD changes:** — · **API/data changes:** as P1-02, P2-01.
- **Implementation tasks:** found empirically by running Compose; one bug found and fixed (event replay).
- **Unit tests:** `TestEventsWithoutSinceAreLiveOnly`. · **Integration tests:** `compose.sh` ×2 builds.
- **Android device tests:** PENDING. · **Security checks:** — · **Performance checks:** —
- **Definition of Done:** MVP commands pass. · **Rollback:** —
- **Evidence:** e3174bb. · **Status:** DONE (host)

### P4-03 — Compose project semantics
- **Purpose:** scoped, idempotent operations.
- **Expected behaviour:** `down` removes only the project's containers and network; a second `up -d` recreates nothing.
- **Existing behaviour:** — · **Dependencies:** P4-02 · **Affected modules:** — (labels already stored)
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** tests only.
- **Unit tests:** — · **Integration tests:** `compose.sh` (bystander survives, ids unchanged). · **Android device tests:** PENDING
- **Security checks:** — · **Performance checks:** —
- **Definition of Done:** tests pass. · **Rollback:** — · **Evidence:** e3174bb. · **Status:** DONE (host)

### P4-04 — Compose MVP commands
- **Purpose:** config, pull, up, up -d, ps, logs, exec, stop, start, restart, down.
- **Existing behaviour:** — · **Expected behaviour:** as Compose documents. · **Dependencies:** P4-02
- **Affected modules:** — · **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** `tests/regression/compose.sh`.
- **Unit tests:** — · **Integration tests:** 26/26 upstream v5.5.1, 26/26 Debian 2.26.1-4. · **Android device tests:** PENDING
- **Security checks:** published on loopback. · **Performance checks:** —
- **Definition of Done:** host and device pass. · **Rollback:** — · **Evidence:** e3174bb. · **Status:** TESTING (device PENDING)

### P4-05 — Multi-service integration
- **Purpose:** real-world proof.
- **Expected behaviour:** deterministic two-service stack (done) and the TON API + Explorer stack.
- **Existing behaviour:** — · **Dependencies:** owner's images and their ports/env
- **Affected modules:** fixtures · **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** write the TON Compose file with the owner; run 24 h on the QA build.
- **Unit tests:** — · **Integration tests:** two-service fixture. · **Android device tests:** PENDING
- **Security checks:** — · **Performance checks:** idle profile during the 24 h run.
- **Definition of Done:** TON stack healthy 24 h. · **Rollback:** — · **Evidence:** — · **Status:** IN_PROGRESS

### P4-06 — Compose limitations
- **Purpose:** explicit errors, never silent acceptance.
- **Expected behaviour:** build, healthcheck, internal networks, IPv6, custom IPAM, resource limits, privileged refused with reasons.
- **Existing behaviour:** — · **Dependencies:** — · **Affected modules:** engine, api
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** refusals; matrix entries.
- **Unit tests:** refusal tests in engine and api. · **Integration tests:** — · **Android device tests:** —
- **Security checks:** internal networks refused rather than half-honoured. · **Performance checks:** —
- **Definition of Done:** each refusal tested and listed. · **Rollback:** — · **Evidence:** `COMPATIBILITY_MATRIX.md`. · **Status:** DONE

### P4-07 — Health checks (new)
- **Purpose:** many Compose files use `healthcheck:` and `depends_on: condition: service_healthy`; today they fail at create.
- **Existing behaviour:** refused (501).
- **Expected behaviour:** Docker's health state machine (starting/healthy/unhealthy, interval, timeout, retries, start period) driven by one exec per interval, only for containers that define a check.
- **Dependencies:** exec · **Affected modules:** engine, api (`State.Health`), events (`health_status`)
- **HLD/LLD changes:** to write · **API/data changes:** `State.Health`
- **Implementation tasks:** per-container timer while running; cancelled on exit.
- **Unit tests:** fake runtime health sequences. · **Integration tests:** Compose `service_healthy`.
- **Android device tests:** cost of one PRoot exec per interval, measured.
- **Security checks:** — · **Performance checks:** the only periodic work in the engine: document its cost per interval.
- **Definition of Done:** Compose `service_healthy` works; cost measured. · **Rollback:** keep refusing.
- **Evidence:** — · **Status:** PLANNED (highest-value next Compose ticket)

---

## Phase 5 — Android management UI

| Ticket | Title | Status |
|---|---|---|
| P5-01 | Images screen: list, sizes, tags, usage, pull, inspect, safe delete | PLANNED |
| P5-02 | Volumes screen: list, attachment, create, delete (in-use protected) | PLANNED |
| P5-03 | Networks screen: user networks, members, addresses (no fictitious isolation) | PLANNED |
| P5-04 | Stacks screen: Compose projects from labels, start/stop/restart, logs | PLANNED |
| P5-05 | UX: graphite/silver/cyan, event-driven refresh instead of the 3 s poll | PLANNED |

Each follows the ticket format when started. Purpose, expected behaviour
and tests are as listed in the programme directive §10; every screen is a
client of the API (no state of its own), lazy, and costs nothing when
closed. The Web Panel (P6) already provides these views in a browser and
its views are the reference for the screens.

---

## Phase 6 — ThothDock Web Panel

### P6-01 — Panel architecture
- **Purpose:** browser management without exposing the engine socket.
- **Existing behaviour:** —
- **Expected behaviour:** separate process, engine socket only, fixed allow list (ADR-0005).
- **Dependencies:** P1-02 · **Affected modules:** `internal/panel`, `cmd/thothdock/panel.go`
- **HLD/LLD changes:** HLD §6, LLD §7 · **API/data changes:** panel HTTP surface.
- **Implementation tasks:** server, engine client, views.
- **Unit tests:** `internal/panel` (no generic proxy). · **Integration tests:** `tests/panel/e2e.sh`.
- **Android device tests:** PENDING. · **Security checks:** SECURITY_MODEL N–S.
- **Performance checks:** zero when off; +0.72 MB stripped binary.
- **Definition of Done:** host tests pass. · **Rollback:** do not start it. · **Evidence:** 39e1078. · **Status:** DONE (host)

### P6-02 — Authentication and pairing
- **Purpose:** only the owner's browsers.
- **Expected behaviour:** TLS, fingerprint, one-time code, hashed revocable sessions, CSRF, Origin.
- **Existing behaviour:** — · **Dependencies:** P6-01 · **Affected modules:** `internal/panel/auth.go`, `tls.go`
- **HLD/LLD changes:** ADR-0005 · **API/data changes:** `POST /pair`, `/api/logout`, `--revoke-all`.
- **Implementation tasks:** as listed.
- **Unit tests:** `TestPairingRules`, `TestSessionsAreHashedPersistedAndRevocable`, `TestStateChangesNeedOriginAndCSRF`.
- **Integration tests:** browser: wrong code refused, pairing, sign-out. · **Android device tests:** PENDING (LAN browser).
- **Security checks:** crypto/rand, constant-time compares, 0600 files. · **Performance checks:** —
- **Definition of Done:** tests pass. · **Rollback:** — · **Evidence:** 39e1078. · **Status:** DONE (host)

### P6-03 — Dashboard
- **Expected behaviour:** engine, running/stopped, images, volumes, networks, stacks, memory, storage, load, live events.
- **Purpose/Existing/Dependencies:** as P6-01 · **Affected modules:** panel views, assets
- **HLD/LLD changes:** — · **API/data changes:** `/api/summary`, `/api/events`
- **Implementation tasks:** done. · **Unit tests:** summary, SSE. · **Integration tests:** browser tiles, events tab.
- **Android device tests:** PENDING (which `/proc` figures an app may read). · **Security checks:** figures the platform withholds are omitted, never guessed.
- **Performance checks:** event-driven refresh. · **Definition of Done:** host pass. · **Rollback:** — · **Evidence:** `phase6/` screenshots. · **Status:** DONE (host)

### P6-04 — Container management
- **Expected behaviour:** start, stop, restart, delete (confirmed), logs; stack start/stop/restart.
- **Purpose/Existing/Dependencies:** as P6-01 · **Affected modules:** panel
- **HLD/LLD changes:** — · **API/data changes:** `/api/containers/*`, `/api/stacks/*`
- **Implementation tasks:** done. · **Unit tests:** actions, path validation, unlisted actions 404. · **Integration tests:** browser Stop/Start/Logs.
- **Android device tests:** PENDING. · **Security checks:** CSRF/Origin on every change. · **Performance checks:** —
- **Definition of Done:** host pass. · **Rollback:** — · **Evidence:** 39e1078. · **Status:** DONE (host)

### P6-05 — Compose stack deployment
- **Purpose:** paste YAML, deploy.
- **Existing behaviour:** —
- **Expected behaviour:** validation shows unsupported keys before deploy; deployment runs the guest's Compose client, never a YAML interpreter in the panel.
- **Dependencies:** P4-01, a way to run the guest's Compose from the app · **Affected modules:** panel, Android
- **HLD/LLD changes:** to write · **API/data changes:** `/api/stacks` POST
- **Implementation tasks:** design the guest-side runner (the panel runs in the app context, Compose in the guest).
- **Tests:** to define. · **Security checks:** no host-side instruction from YAML is executed; binds limited by `--allow-bind`.
- **Performance checks:** — · **Definition of Done:** — · **Rollback:** — · **Evidence:** — · **Status:** PLANNED

### P6-06 — Lightweight operation
- **Expected behaviour:** off by default; stopping frees listener and pairing file.
- **Purpose/Existing/Dependencies:** — · **Affected modules:** panel, Android `WebPanel.java`
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** done. · **Unit tests:** — · **Integration tests:** e2e: listener gone, pairing file removed after stop.
- **Android device tests:** PENDING (panel stops with the app, no process left). · **Security checks:** —
- **Performance checks:** engine idle unchanged with the panel compiled in (final gate).
- **Definition of Done:** device row. · **Rollback:** — · **Evidence:** e2e output. · **Status:** TESTING

### P6-07 — Android control of the panel (new)
- **Purpose:** start/stop and pair from the phone.
- **Expected behaviour:** Containers screen > Web Panel: "This phone only" or "Wi-Fi network" (warning; Wi-Fi/Ethernet IPv4 only); URL, code, expiry, fingerprint; New code; Stop; stopped with the engine.
- **Existing behaviour:** — · **Dependencies:** P6-01 · **Affected modules:** Garden `WebPanel.java`, `ContainersActivity`, `ThothDock.stop()`
- **HLD/LLD changes:** — · **API/data changes:** —
- **Implementation tasks:** done; forks from a dedicated thread because PR_SET_PDEATHSIG follows the forking thread.
- **Unit tests:** garden-common 367/0, garden-debian 33/0 (no new unit test for the dialog).
- **Integration tests:** — · **Android device tests:** PENDING. · **Security checks:** never 0.0.0.0, never cellular.
- **Performance checks:** — · **Definition of Done:** device round. · **Rollback:** — · **Evidence:** Garden 07c8498. · **Status:** IMPLEMENTED

---

## Phase 7 — Android Server Mode

| Ticket | Title | Status |
|---|---|---|
| P7-01 | Owner-enabled Server Mode: boot start of the foreground service and engine, honest restrictions text | PLANNED |
| P7-02 | Restore eligible workloads at boot (uses P3-03 unchanged) | PLANNED (engine side DONE) |
| P7-03 | Battery-aware operation: no wake locks, no polling, battery-optimisation exemption only with the owner's consent | PLANNED |
| P7-04 | Safeguards: low-storage refusal of pulls/creates (the free-space floor exists for pulls), thermal status via `PowerManager` thermal API; advisory limits clearly labelled as not enforced | PLANNED |
| P7-05 | Long-run tests: 24 h, 48 h, screen off, network loss, backgrounding, low storage, battery transitions — recorded only after they have run | PLANNED |

---

## Phase 8 — Security, hardening and compatibility

| Ticket | Title | Status |
|---|---|---|
| P8-01 | Audit: hosts-file injection (done), panel (done in P6), OCI extraction and ceilings (v0.1.1), path traversal, API fuzzing of the new endpoints | IN_PROGRESS |
| P8-02 | Compatibility tests against the Engine API specification for the implemented endpoints; differential runs against a real dockerd where the owner allows it | PLANNED |
| P8-03 | P2-04b connection filtering by membership in the PRoot extension | PLANNED |
