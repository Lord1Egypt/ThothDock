# ADR-0005 — The Web Panel is a separate, off-by-default process with TLS and pairing

- Status: ACCEPTED (2026-10-10)
- Requirements: FR-WEB-01..08, NFR-01, NFR-07
- Tickets: P6-01..P6-06

## Decision

- `thothdock panel` is a subcommand of the same binary, so it adds no APK
  size. It runs as its own process and talks to the engine only through the
  engine's Unix socket. When it is stopped, it leaves no listener, session or
  goroutine in the engine.
- It serves HTTPS only. On first start it creates a self-signed ECDSA P-256
  certificate (stdlib `crypto/x509`), stored in the panel's state directory
  (0700). The certificate's SHA-256 fingerprint is shown where the panel is
  started, so the browser warning can be checked against it.
- Pairing: the panel prints a one-time 8-digit pairing code (from
  `crypto/rand`), valid for 10 minutes and 5 attempts. A correct code yields a
  random 256-bit session token in an `HttpOnly; Secure; SameSite=Strict`
  cookie. Sessions are stored hashed (SHA-256), expire after 30 days idle and
  can all be revoked (`thothdock panel --revoke-all`).
- Every state-changing request needs the session cookie, an `Origin` that
  matches the panel's own host, and an `X-ThothDock-CSRF` header equal to a
  per-session token. Comparisons use `crypto/subtle`.
- The panel exposes a fixed allow list of engine operations, never a generic
  proxy of the Docker API. Unsupported operations do not exist in it.
- It binds `127.0.0.1` unless given an explicit LAN address with
  `--listen`. The Android app shows a warning before it does that.
- Static assets are embedded (`embed`), with a strict Content-Security-Policy:
  no inline scripts and no third-party origins.

## Rejected

- **Serving the panel from the engine process.** It saves about 8 MiB while
  the panel runs, but it puts a LAN-facing parser in the process that holds
  every container. A crash or leak there would take the workloads with it.
- **Plain HTTP with a password.** Credentials and session cookies would cross
  the LAN in clear text.
