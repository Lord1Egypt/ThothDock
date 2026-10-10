# Security model — next generation

This extends `docs/SECURITY_MODEL.md` (v0.1.1), which still holds in full:
**a ThothDock container is not a security boundary.** It runs as the Android
app's uid under ptrace translation. The real boundary is the app sandbox.

## Threats the new work touches

| | Threat | Stance | Mechanisms and residual risk |
|---|---|---|---|
| **G** | A workload reachable beyond what `-p` says | **Defended for network-attached containers; documented gap on the device network** | With `--net-ip`, a bind to `0.0.0.0`/`::` lands on the container's 127.77.x.y address, which nothing off the device can route to (tested: no new wildcard listener). Containers on the default/host network keep v0.1.1 behaviour: a `0.0.0.0` listener is reachable on the device's addresses. |
| **B** | Accidental interference between containers | **Improved** | Network-attached containers have their own address and a private `localhost`, so two of them cannot collide on a port or reach each other's `localhost` services by accident. Name resolution follows network membership. |
| **J** | A container reaching another container it shares no network with | **Not defended** | Membership controls which **names** a container sees, not which **addresses** it can connect to: any process can connect to any 127.77.x.y address. Planned (P2-04b): a connect filter in the PRoot extension. Even then it would be a convenience enforced by the tracer, not a boundary: a process making syscalls PRoot does not see (io_uring where the kernel permits it to apps) is not filtered. |
| **K** | Address rewriting bypassed | **Accepted** | The rewrite happens in PRoot's syscall stops for `bind`, `connect` and `sendto`. `sendmsg`/`sendmmsg` destinations and io_uring are not rewritten. A bypass gives a workload no more than v0.1.1 had (the device network); it gains no privilege. |
| **L** | Hosts-file injection through names | **Defended** | Container names, hostnames and aliases are validated before they reach `/etc/hosts` (no whitespace or line breaks can pass); the first line naming a host wins, so a peer alias cannot shadow `localhost` or `host.docker.internal`. Tested. |
| **M** | Restart policy turning a crash loop into a battery drain | **Bounded** | Backoff 100 ms doubling to 60 s, reset only after a 10 s run: at most one start per minute after the first ~10 attempts. |
| **N** | Web Panel: network attacker on the LAN | **Defended within its stated scope** | HTTPS only (TLS 1.2+), certificate fingerprint shown on the phone. Pairing needs a one-time 8-digit code (10⁸ values, 5 attempts per code, one try per second per address, 10-minute lifetime, single use). Sessions are 256-bit random tokens, stored only as SHA-256 hashes (0600). The panel binds 127.0.0.1 by default; on Wi-Fi it binds the Wi-Fi address only, after a warning, never 0.0.0.0 and never cellular. |
| **O** | Web Panel: a malicious web page in the owner's browser (CSRF, clickjacking) | **Defended** | State changes need the panel's own `Origin` and a per-session CSRF header (constant-time compare). Cookies are `HttpOnly; Secure; SameSite=Strict`. `frame-ancestors 'none'`, `X-Frame-Options: DENY`. |
| **P** | Web Panel: XSS through engine data | **Defended** | Strict CSP (`script-src 'self'`, no inline code); the page inserts every engine value with `textContent` (container names, labels, log lines). Browser-tested for CSP violations. |
| **Q** | Web Panel as a path to the Docker API | **Defended** | A fixed allow list of operations, each validating its path parameters (`[a-zA-Z0-9][a-zA-Z0-9_.-]`); no generic proxy exists (tested: `/v1.41/...` paths are 404). The panel cannot create containers, pull images, bind host paths or exec. |
| **R** | Web Panel: someone with the phone unlocked | **Not defended** | The owner's phone shows the pairing code by design. Anyone who can use the app can already use the terminal. |
| **S** | TLS trust on first use | **Accepted risk** | A self-signed certificate cannot be verified by the browser. The owner must compare the fingerprint once; skipping that leaves the first pairing open to an active LAN attacker. A pinned per-device CA is a possible later improvement. |

## What still is not defended

Everything listed in the v0.1.1 model as not defended (a compromised
container process, other apps reaching `127.0.0.1` published ports, no
cgroups) is unchanged. Network-attached containers' addresses are also on
the device's loopback: any app on the phone can connect to a container's
127.77.x.y address, exactly as it can reach a published port.
