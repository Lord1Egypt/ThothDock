# Docker compatibility matrix

Generated from `compatibility.json` by `render_matrix.py`; edit the JSON. Updated 2026-10-10, engine feature/nextgen (39e1078 and later).

A status comes only from a test, never from reading code. API acceptance is not behavioural equivalence, and neither is a security boundary (`SECURITY_MODEL.md`).

| Status | Meaning |
|---|---|
| `NATIVE_EQUIVALENT` | Behaves as dockerd for the tested workloads, through the same mechanism class |
| `USERSPACE_EQUIVALENT` | Same observable behaviour for the tested workloads, implemented in user space (PRoot, forwarders, hosts files) |
| `PARTIAL` | Works with a stated semantic difference |
| `UNSUPPORTED` | Refused with an explicit error; never silently accepted |
| `NOT_TESTED` | Implemented or accepted, but no test has established the behaviour |

| Key | Verified by |
|---|---|
| T | go test -race ./... (unit and API tests) |
| H | host end-to-end with the stock Docker CLI and a real PRoot (tests/regression, tests/smoke) |
| C | host end-to-end with Docker Compose v5.5.1 and Debian 2.26.1-4 (tests/regression/compose.sh) |
| B | browser end-to-end (tests/panel/e2e.sh) |
| P | on the SM-A165F phone (Android 16) |

Totals: NATIVE_EQUIVALENT 9, NOT_TESTED 1, PARTIAL 6, UNSUPPORTED 10, USERSPACE_EQUIVALENT 9

| Feature | Status | Verified | Tests | Android | Security | Overhead | Known failure modes |
|---|---|---|---|---|---|---|---|
| docker version, info, _ping | `NATIVE_EQUIVALENT` | T H P | tests/smoke/docker-cli-smoke.sh | — | — | none | — |
| docker pull (index, platform selection, digests, bearer and basic auth) | `NATIVE_EQUIVALENT` | T H P | internal/image, tests/smoke | Android trust store | digest and size verified before use; extraction ceilings | network and storage during the pull only | zstd layers refused; foreign layers refused |
| --platform other than the device's | `UNSUPPORTED` | T | internal/api | no emulation | — | — | 400 with the reason |
| image inspect, tag, rmi, history, images --filter | `NATIVE_EQUIVALENT` | T H | internal/image, internal/api | — | — | — | filters: reference, dangling, label only |
| docker build / compose build | `UNSUPPORTED` | T | internal/api (501) | — | — | — | 501; planned after the runtime work |
| push, import, commit, export, cp, diff | `UNSUPPORTED` | T | internal/api (501) | — | — | — | 501 planned |
| create, start, run, run -d, --rm, stop, kill, restart, wait, rm, prune | `NATIVE_EQUIVALENT` | T H P | internal/engine, tests/regression/lifecycle.sh | containers end with the app | — | one blocked goroutine per running container | exit 126/127 like dockerd |
| explicit states and refused illegal transitions | `USERSPACE_EQUIVALENT` | T | internal/engine TestTransitionGuardRefusesIllegalMoves | — | — | none | 409 instead of a corrupted record |
| -i, -t, attach, start -a, resize | `NATIVE_EQUIVALENT` | T H P | internal/api, tests/regression/lifecycle.sh | — | — | — | detach keys not implemented |
| logs -f -t --tail --since --until | `NATIVE_EQUIVALENT` | T H P | internal/logs | — | logs never contain request bodies or credentials | — | json-file only; rotation at 10 MiB |
| docker exec (-i -t -u -w -e -d) | `PARTIAL` | T H P | tests/smoke | — | — | one PRoot per exec | no shared PID view: the exec is not in the container's process table |
| --user | `PARTIAL` | T H | internal/engine TestUserMapping | — | fake uid/gid (PRoot --change-id); no real privilege separation | — | — |
| --restart no\|always\|unless-stopped\|on-failure[:N] | `USERSPACE_EQUIVALENT` | T H | internal/engine restart_test.go, tests/regression/restart-policies.sh | restored when the engine starts; nothing restarts while Android keeps the app stopped | — | one timer only while a container waits to restart; backoff to 60 s | kill with a signal other than SIGKILL is not a manual stop (dockerd treats every kill as one) |
| docker update --restart | `NATIVE_EQUIVALENT` | T H | internal/api TestUpdateRestartPolicyAndRefusedResources, tests/regression/restart-policies.sh | — | — | — | — |
| --memory, --cpus, --pids-limit, blkio, ulimits; docker update of them | `UNSUPPORTED` | T H | internal/engine TestUnsupportedFeaturesAreRefused | no cgroups for apps | a container can use all CPU and memory the app may use | — | 501 |
| --privileged, --cap-add/drop, --device, --gpus, --security-opt, --sysctl, --read-only, --tmpfs, --shm-size | `UNSUPPORTED` | T | internal/engine TestUnsupportedFeaturesAreRefused | — | — | — | 501 |
| HEALTHCHECK / healthcheck: / depends_on condition service_healthy | `UNSUPPORTED` | T | internal/engine | — | — | would be one exec per interval | a Compose file with healthcheck fails at create (ticket P4-07) |
| pause, unpause, stats, top | `UNSUPPORTED` | T | internal/api | no freezer cgroup | — | — | 501; stats/top from /proc planned |
| docker volume create/ls/inspect/rm/prune, -v NAME:/path | `NATIVE_EQUIVALENT` | T H C P | internal/volume, tests/smoke, tests/regression/compose.sh | — | names validated; removal never follows symlinks | — | no copy-up; no drivers; :ro refused |
| -v /host:/path | `PARTIAL` | T | internal/engine | only below serve --allow-bind directories | canonicalised; never the data root | — | :ro refused |
| -p [ip:]host:container[/tcp] | `USERSPACE_EQUIVALENT` | T H P | tests/smoke, tests/regression/networks.sh | 127.0.0.1 by default | LAN publishing needs serve --allow-publish-nonlocal | a goroutine pair per open connection; nothing while idle | — |
| -p .../udp, -P with UDP ports | `UNSUPPORTED` | T H | tests/smoke | — | — | — | 501 (ticket P2-06) |
| default / bridge / host network mode | `PARTIAL` | T H P | internal/engine, tests/regression/networks.sh | — | a workload on 0.0.0.0 is reachable on the device's addresses | none | every such container shares the device network, as --network host |
| docker network create/ls/inspect/rm/prune | `USERSPACE_EQUIVALENT` | T H C | internal/network, internal/engine networks_test.go, tests/regression/networks.sh | needs Garden PRoot with patch 0009 (--net-ip); device verification PENDING | not a security boundary (ADR-0002) | none: rewriting happens in syscall stops PRoot already takes | internal, IPv6, custom IPAM, non-bridge drivers refused with 501 |
| per-container address, private localhost, two containers on one port | `USERSPACE_EQUIVALENT` | H | tests/garden-common/proot/net-ip-check.sh (AndroidThothTerm), tests/regression/networks.sh | device verification PENDING | a 0.0.0.0 listener is not exposed beyond loopback | none measured idle | peers appear to come from 127.0.0.1; ports <1024 seen as +30000 by getsockname; sendmsg/sendmmsg destinations not rewritten |
| names, --network-alias, Compose service names | `PARTIAL` | T H C | tests/regression/networks.sh, tests/regression/compose.sh | — | names written to /etc/hosts are validated | hosts files rewritten on create, connect, disconnect, remove | no DNS server at 127.0.0.11: resolvers that bypass /etc/hosts (nginx resolver directive) do not see names; stopped peers stay listed |
| docker network connect/disconnect | `PARTIAL` | T H | internal/engine TestConnectDisconnect, tests/regression/networks.sh | — | — | — | a running device-network container must be stopped first (409) |
| host.docker.internal, --add-host x:host-gateway | `USERSPACE_EQUIVALENT` | T H | tests/regression/networks.sh | — | reaches the device's loopback services | — | — |
| --network none, --network container:X, links | `UNSUPPORTED` | T | internal/engine TestNetworkCreateRefusals | — | — | — | 501; container:X planned |
| docker events with filters, since, until | `NATIVE_EQUIVALENT` | T H C | internal/events, internal/api events_test.go, tests/regression/restart-policies.sh | — | — | none without subscribers; 1024-event ring | a subscriber more than 256 events behind is disconnected; history lost at daemon restart (as dockerd) |
| compose config/pull/up/up -d/ps/logs/exec/stop/start/restart/down [-v] | `USERSPACE_EQUIVALENT` | C | tests/regression/compose.sh with tests/fixtures/compose/two-service | apt install docker-compose in the guest; device verification PENDING | down is scoped to the project's labels | none when Compose is not running | build, healthcheck, internal networks, IPv6, resource limits refused |
| TON API + Explorer as a Compose stack | `NOT_TESTED` | — | P4-05 (pending: needs the owner's images and the device) | — | — | — | — |
| ThothDock Web Panel | `USERSPACE_EQUIVALENT` | T B | internal/panel, tests/panel/e2e.sh | Containers screen > Web Panel; device verification PENDING | TLS, one-time pairing code, hashed sessions, CSRF+Origin, allow list | zero when off (separate process); +0.7 MB binary | stack deploy from YAML not implemented (P6-05) |
| Android Containers screen | `USERSPACE_EQUIVALENT` | P | garden-debian unit tests; device QA | — | — | polls every 3 s only while visible | no Images/Volumes/Networks/Stacks screens yet (P5) |
| swarm, services, secrets, configs, plugins | `UNSUPPORTED` | T | internal/api | — | — | — | 501 |
