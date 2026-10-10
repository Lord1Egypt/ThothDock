# ThothDock next generation — low-level design

Companion to `SYSTEM_HLD.md`. It specifies data formats, algorithms and exact
rules so an implementation can be checked against them.

## 1. Container record additions (`containers/<id>/config.json`)

| Field | Type | Meaning | Ticket |
|---|---|---|---|
| `state.status` | adds `"restarting"` | waiting for a policy restart | P3-02 |
| `state.manuallyStopped` | bool | set by stop/kill via the API; cleared by start/restart | P3-01 |
| `state.restarting` | bool | mirrors `status == restarting` for the API | P3-02 |
| `restartCount` | int | policy restarts and `docker restart` (as dockerd) | P3-02 |
| `networks` | map name → `{networkId, aliases[], ipAddress}` | attached user-defined networks | P2-01 |
| `netIP` | string | the container's loopback address, `""` when only on the device network | P2-02 |

Old records lack these fields and decode to their zero values, which means
exactly the v0.1.1 behaviour (`no` policy, device network). No migration is
needed and none is written.

### Allowed transitions (P1-01)

| From \ To | starting | running | exited | restarting | removing | created | failed |
|---|---|---|---|---|---|---|---|
| created | start | — | — | — | rm | — | — |
| starting | — | started | exit before running | — | — | start failed | — |
| running | — | — | exit | — | rm -f | — | — |
| exited | start | — | — | policy | rm | — | — |
| restarting | backoff end | — | stop/kill | — | rm -f | — | — |
| removing | — | — | — | — | — | — | removal error |
| failed | start | — | — | — | rm | — | — |

`engine.transition(c, to)` checks this table; a refused transition returns
`409 Conflict` and is logged. Recovery at start (`reconcile`) is the only
writer allowed to move `running/starting/restarting → exited` without a
process exit, because the process no longer exists.

## 2. Events (P1-02)

```go
type Event struct {
    Type     string            // container | image | network | volume
    Action   string            // create, start, die, stop, kill, restart, destroy, ...
    ID       string            // Actor.ID
    Attrs    map[string]string // Actor.Attributes: name, image, exitCode, signal, labels...
    Time     time.Time
}
```

JSON on `/events` is dockerd's: `{"Type","Action","Actor":{"ID","Attributes"},
"scope":"local","time","timeNano"}` plus the legacy `status`, `id`, `from`
for container and image events. One JSON object per line, flushed after each.

Bus: `ring [1024]Event`, `next uint64`, `subs map[*sub]struct{}`, one mutex.
`Publish` appends to the ring, then does a non-blocking send to each
subscriber's 256-slot channel. A full channel closes that subscriber
(`slow consumer`), which ends its HTTP response. `Subscribe(since)` returns
the ring's events after `since` and the live channel, atomically under the
mutex, so no event is missed or duplicated between replay and live.

Filters: `type`, `event`, `container` (ID prefix or name), `image`,
`label` (`k` or `k=v`), `network`, `volume`. `since`/`until` accept Unix
seconds (`1700000000` or `1700000000.123456789`) and Go durations relative to
now (`10m`), as the CLI sends them.

## 3. Restart supervisor (P3)

```
decide(policy, exitCode, manuallyStopped, restartCount) bool:
  if manuallyStopped: return false
  switch policy.Name:
    "always", "unless-stopped": return true
    "on-failure": return exitCode != 0 &&
                         (policy.MaximumRetryCount == 0 || restartCount < policy.MaximumRetryCount)
  return false

backoff(previous, ranFor):
  if ranFor >= 10s: return 100ms               // healthy run resets the delay
  next = previous*2 (first: 100ms), capped at 60s
```

On exit, the monitor (already running per container) calls `decide`. When it
returns true, the container moves to `restarting`, an `AfterFunc` timer is
armed (one per waiting container, none otherwise), and the timer calls
`Start` only if the state is still `restarting`. `docker stop` or `kill` on a
restarting container cancels the timer and records `exited` with
`manuallyStopped`. `docker rm -f` cancels it and removes.

Restore at engine start (P3-03), after `reconcile`:

```
for each container in creation order:
  if wasRunning (state was running/starting/restarting before reconcile)
     and decide(policy, 137, manuallyStopped, restartCount):
        Start (asynchronously, so the API is up at once)
```

`always` also restores a container that was manually stopped, as dockerd does
when it starts. That is the only difference between `always` and
`unless-stopped`.

## 4. Network model (P2-01)

`networks.json` (atomic JSON) holds user-defined networks:

```json
{"networks": {"<64-hex id>": {"name": "myapp_default", "id": "...", "created": "...",
  "driver": "bridge", "labels": {...}, "options": {...}, "internal": false, "attachable": false}}}
```

Built-in names `host`, `bridge` and `none` are not stored. They always exist
in `docker network ls` and keep v0.1.1 semantics: `host` and `bridge` both
mean the device network, `none` is refused at create (501).

IPAM: one pool, `127.77.0.0/16`. `127.77.0.0`, `127.77.0.1` (gateway) and
`127.77.255.255` are never allocated. An address belongs to a container from
the moment it is first attached to a user network until it is removed;
allocation picks the lowest free address. All networks report subnet
`127.77.0.0/16` and gateway `127.77.0.1` in inspect, because the address is
per container rather than per network (ADR-0002).

Attach rules:
- `create` with `NetworkMode` = a user network, or `NetworkingConfig.EndpointsConfig`
  naming one or more user networks, attaches at create.
- `POST /networks/{id}/connect` attaches. For a running container the hosts
  files are rewritten at once; a new address would need a restart, so a
  running container that has no address yet (it started on the device
  network) is refused with 409 and an explanation.
- `disconnect` removes the membership and rewrites hosts files. The address
  stays while the container is attached to any user network.
- A network with attached containers cannot be removed (409), as dockerd.

### hosts file (P2-03)

For container C, written to `containers/<id>/hosts` with
`store.WriteFileAtomic` (PRoot opens it fresh on every lookup):

```
127.0.0.1   localhost
::1         localhost ip6-localhost ip6-loopback
<C.ip>      <C.hostname> <C.name>
127.77.0.1  host.docker.internal gateway.docker.internal
<P.ip>      <P.name> <P.aliases...> <P.id[:12]>     for each peer P sharing a network with C
<ExtraHosts lines>
```

A peer appears only while it has an address. Names that collide (two
networks each having a `db`) resolve to the first network in sorted order, as
the first matching hosts line wins. This is a documented difference from
Docker's per-network DNS.

Rewrites happen on: create (self), start/stop of a peer (ports do not
matter, addresses do not change, so only attach, detach and remove trigger
rewrites), connect, disconnect, remove.

### Port publishing for an addressed container (P2-05)

`portmap.Binding{HostIP, HostPort, Target: net.JoinHostPort(C.ip, shift(port))}`
where `shift(p) = p + 30000` for `1 <= p <= 1023`, else `p`. Passthrough
(host port == container port) does not exist for addressed containers,
because their addresses differ.

## 5. PRoot `--net-ip` extension (Garden patch 0009, P2-02)

Option: `--net-ip=127.77.X.Y`. The handler rejects (PRoot exits non-zero)
any address outside `127.77.0.0/16`, the network address, the gateway and the
broadcast address.

Callback on `SYSCALL_ENTER_END` (after PRoot's own Unix-path translation,
skipped if that failed) for `bind`, `connect` and `sendto` with a non-NULL
address. None of these is a new stop: PRoot's core filter already stops on
each of them.

Rules, applied to a copy of the socket address that is pushed with
`alloc_mem` and swapped into the argument register (the tracee's buffer is
never written):

| Syscall | Address | Rewritten to |
|---|---|---|
| bind | IPv4 `0.0.0.0` or `127.0.0.0/8` | `own:shift(port)` |
| bind | IPv6 `::` or `::1`, socket dual-stack | `::ffff:own:shift(port)` |
| bind | IPv6 `::` or `::1`, socket V6ONLY | `[::1]:0` (an inert IPv6 listener) |
| bind | v4-mapped `::ffff:a.b.c.d` | IPv4 rules on `a.b.c.d`, kept mapped |
| connect, sendto | IPv4 `127.77.0.1` | `127.0.0.1:port` (gateway, no shift) |
| connect, sendto | IPv4 other `127.77.0.0/16` | same address, `shift(port)` |
| connect, sendto | IPv4 `0.0.0.0` or other `127.0.0.0/8` | `own:shift(port)` |
| connect, sendto | IPv6 `::1` or `::` | `::ffff:own:shift(port)` |
| connect, sendto | v4-mapped | IPv4 rules, kept mapped |
| any | anything else | unchanged |

`shift(p) = p + 30000` for `1 <= p <= 1023`; port 0 is never shifted.

V6ONLY is read by duplicating the tracee's descriptor with
`pidfd_open` + `pidfd_getfd` and calling `getsockopt(IPV6_V6ONLY)` on the copy.
Where those syscalls fail (kernels before 5.6), the socket is treated as
dual-stack. A V6ONLY wildcard bind then fails with `EINVAL`, which is visible
to the workload and documented.

Not rewritten: `sendmsg` destinations, `sendmmsg`, io_uring, raw syscalls on
32-bit x86 (`socketcall`). These are documented in `COMPATIBILITY_MATRIX.md`.

## 6. Engine capability detection

At startup the runtime reads `proot --help` once (it already checks the
required options). `--net-ip` present → `Capabilities.NetIP = true`. Without
it, creating a container on a user network fails with 501:
"this PRoot has no --net-ip (Garden patch 0009); user-defined networks need
it". Everything else keeps working.

## 7. Web Panel (P6)

State directory `<data root>/panel/` (0700):

| File | Content |
|---|---|
| `cert.pem`, `key.pem` (0600) | self-signed ECDSA P-256, SAN = listen host(s), 825-day validity |
| `sessions.json` (0600) | `{sha256(token) hex: {created, lastSeen, csrf}}` |

Pairing: `code = 8 decimal digits from crypto/rand`, held in memory only,
valid 10 minutes, invalid after 5 wrong attempts or one success. Rate limit:
1 attempt per second per remote address.

HTTP surface (all JSON, `Cache-Control: no-store`):

| Method + path | Engine call |
|---|---|
| `POST /pair` | — (code → cookie) |
| `POST /logout` | — |
| `GET /api/summary` | `/info`, `/containers/json?all=1`, `/images/json`, `/volumes`, `/networks` |
| `GET /api/containers` | `/containers/json?all=1` |
| `POST /api/containers/{id}/{start,stop,restart}` | same |
| `DELETE /api/containers/{id}` | `DELETE /containers/{id}?force=1` (the UI confirms) |
| `GET /api/containers/{id}/logs?tail=N` | `/containers/{id}/logs?stdout=1&stderr=1&tail=N` |
| `GET /api/images`, `GET /api/volumes`, `GET /api/networks` | same |
| `GET /api/events` | `/events` (server-sent events to the browser) |

Headers on every response: `Content-Security-Policy: default-src 'self';
script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self';
frame-ancestors 'none'; base-uri 'none'; form-action 'self'`,
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
`Strict-Transport-Security: max-age=31536000`.
