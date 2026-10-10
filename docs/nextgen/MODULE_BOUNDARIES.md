# Module boundaries

Dependencies point downward only. A package may import those below it, never
those above. `go vet` and the build enforce cycles; this table is the
intent reviewers check.

```
cmd/thothdock            CLI: serve, panel, doctor, gc, version
  internal/panel         Web Panel: HTTPS, pairing, allow-listed views   -> engine socket only (no engine import)
  internal/api           Docker Engine API over the engine
    internal/engine      containers, lifecycle, policies, networks glue, ports, exec
      internal/network   network store + address pool (no engine knowledge)
      internal/events    event bus (no dependencies)
      internal/volume, image, logs, portmap, runtime, securefs, store, procid, oci, registry, layer
      internal/errdefs, platform, version
```

| Rule | Why |
|---|---|
| `internal/panel` talks to the engine over the Unix socket, never through Go imports of `engine` or `api` (its tests may) | the panel stays a separate process with the socket as its only authority (ADR-0005) |
| `internal/network` and `internal/events` import nothing from `engine` | both are independently testable; the engine owns membership and publishing |
| Only `engine` writes container records | one owner for durable state |
| Only `runtime` knows PRoot's command line | a different backend replaces one package |
| Android code is a client of the API, plus process supervision | the screens keep no state of their own, so they always agree with the CLI |
| The PRoot extension lives in Garden (patch 0009) and is optional | without it, everything but user networks works; the engine detects it from `proot --help` |
