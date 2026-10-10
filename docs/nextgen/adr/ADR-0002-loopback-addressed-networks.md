# ADR-0002 — User-defined networks give each container its own loopback address, enforced in PRoot

- Status: ACCEPTED (2026-10-10)
- Requirements: FR-NET-01..07, NFR-01, NFR-06
- Tickets: P2-01, P2-02, P2-03, P2-04, P2-05

## Context

Every ThothDock container shares the device network today. Consequences:

1. Two containers cannot both listen on the same port (two web servers on 80,
   two databases on 5432).
2. A workload that binds `0.0.0.0` is reachable from the LAN whether or not
   it is published (threat G in `docs/SECURITY_MODEL.md`).
3. `localhost` inside a container is the device's localhost, shared by every
   container and every app.
4. There are no network names, so Compose's `http://api:3000` cannot resolve.

Kernel network namespaces need privileges an unrooted app does not have. But
Linux routes the whole of `127.0.0.0/8` to the loopback device, and any
unprivileged process may bind any address in it (measured on the Linux host,
2026-10-10; the same kernel rule applies on Android, and the device test of
P2-02 must confirm it on the SM-A165F before the feature ships). Garden's PRoot
already stops the tracee on `bind`, `connect`, `sendto`, `sendmsg` and
`recvfrom` to emulate netlink and translate Unix socket paths, so rewriting
an IP socket address there adds no new ptrace stops.

## Decision

1. Containers attached to a user-defined network (`docker network create`,
   every Compose project) get one address from `127.77.0.0/16`, allocated by
   the engine and stored in the container record.
2. A new PRoot extension, enabled per container with
   `--net-ip=127.77.X.Y`, rewrites socket addresses at syscall entry:
   - `bind` to a wildcard or loopback address (IPv4, IPv6, v4-mapped) binds
     the container's own address instead. A V6ONLY IPv6 wildcard bind becomes
     `[::1]:0`, so the server starts and stays IPv4-reachable through its
     IPv4 socket.
   - `connect` to loopback (`127.0.0.0/8` outside `127.77.0.0/16`, `::1`)
     goes to the container's own address, so `localhost` is private to the
     container, as in Docker.
   - `127.77.0.1` is the gateway: `connect` to it goes to the device's
     `127.0.0.1`, which is what `host.docker.internal` resolves to.
   - Ports 1-1023 on container addresses become `port + 30000` on bind and on
     connect, because Android forbids unprivileged binds below 1024. The engine
     applies the same shift when it forwards `-p` traffic.
   The rewritten address is pushed on the tracee's stack (`alloc_mem`), so
   the application's own buffer is never modified.
3. Names are resolved with per-container `/etc/hosts` files that the engine
   regenerates when a peer joins, leaves or changes address. There is no DNS
   server, because a resolver would need UDP and port 53.
4. The default network mode (no `--network`, `host`, `bridge`) is unchanged:
   the device network, as in v0.1.1. Existing containers keep their exact
   behaviour.
5. `-p` for a network-attached container forwards from the host address to
   the container's address, so host port and container port may be equal and
   several containers may publish the same container port.

## Alternatives rejected

- **`/etc/hosts` names pointing at `127.0.0.1` only.** Zero cost, but port
  collisions and LAN exposure remain, and localhost stays shared.
- **`LD_PRELOAD` socket shim.** Static binaries (Go, Rust musl) bypass it.
- **Userspace TCP/IP stack (slirp, gVisor netstack).** Real isolation of
  addressing, but a resident packet-processing loop per container costs CPU
  and battery continuously. That is the trade ADR-0001 rejects.
- **Rewriting `sendto` destinations for UDP.** Possible later (the stop
  already exists), but UDP inside a container works on its own address
  without it, and cross-container UDP is not needed by the MVP (P2-06).

## Consequences

- No per-connection or per-container process: idle networking costs nothing
  (budget NFR-01).
- A workload's `0.0.0.0` listener is no longer reachable from the LAN unless
  it is published. This closes threat G for network-attached containers.
- **Not a security boundary.** The rewrite is a convenience enforced by the
  same tracer that already translates paths. Raw syscalls the tracer does not
  see (for example through io_uring, where the kernel allows it) are not
  rewritten. `SECURITY_MODEL.md` states this.
- A server sees peers coming from `127.0.0.1`, not from their container
  address (the kernel picks the source address). IP-based allow lists inside
  containers see one source.
- `getsockname` on a shifted low port reports the real port (`30080`).
