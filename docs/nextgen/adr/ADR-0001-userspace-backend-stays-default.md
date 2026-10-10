# ADR-0001 — The PRoot userspace backend stays the default and only execution path

- Status: ACCEPTED (2026-10-10)
- Requirements: NFR-01, NFR-02, NFR-05

## Context

The golden stability baseline (four containers for about two days on a
Samsung Galaxy A16, beside a demanding game) comes from a design with no
virtual machine, no second daemon and no per-container management loop. On the
host, the v0.1.1 daemon with four idle containers used 0 ms of CPU and fewer
than 10 context switches in each 10 s window
(`docs/nextgen/evidence/phase0/`).

QEMU or a full VM would bring real namespaces and cgroups, but it would cost
hundreds of MiB of RAM, steady CPU and battery, and it would not run on stock
unrooted Android without software emulation.

## Decision

`Docker CLI → ThothDock engine → Garden PRoot → Android` remains the only
execution path. Every new subsystem (networking, events, restart supervisor,
Web Panel) is built inside this structure. Any future backend (for example a
VM backend for rooted or KVM-capable devices) must live behind
`runtime.Runtime`, be optional and off by default, and report its capability
differences through the compatibility matrix.

## Consequences

- Kernel-enforced isolation (namespaces, cgroups, seccomp profiles) stays out
  of reach. The security model says so plainly (`SECURITY_MODEL.md`).
- The features that can be built in user space (addressing, discovery,
  restart policies, Compose, management UIs) get the full engineering effort.
