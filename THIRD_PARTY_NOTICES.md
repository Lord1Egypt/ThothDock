# Third-party notices

ThothDock itself is Apache-2.0 (see `LICENSE`).

| Component | Version | License | How it is included |
|---|---|---|---|
| `golang.org/x/sys` (`unix`) | v0.47.0 | BSD-3-Clause (`vendor/golang.org/x/sys/LICENSE`, `PATENTS`) | Vendored under `vendor/` so a build needs no network. `go mod vendor` reproduces it exactly; CI fails if it drifts |

Everything else is the Go standard library. PRoot (GPL-2.0-or-later) is run as a
separate program from the host app and is not part of this repository.
