# ThothDock vision

**ThothDock makes old and mid-range Android phones genuinely useful as
servers without making them unusable as phones.**

It runs real OCI images with the stock Docker CLI and Docker Compose on
unrooted Android, through a userspace engine that costs nothing while it
waits: no virtual machine, no second daemon, no polling loop.

## The evidence this rests on

On a Samsung Galaxy A16 (SM-A165F, Android 16), the owner ran four containers
for about two days: a TON blockchain API, an nginx-based explorer, Alpine and
CentOS Stream 10. During that time they played Wild Rift until the battery
reached 8%, and noticed no game slowdown and no unusual drain from ThothDock.
That is an owner observation, not a benchmark. The host measurement of the
same engine explains it: with four idle containers, the engine and every
container process together used **0 ms of CPU and 0 context switches** in
each 10-second window (`PERFORMANCE_BASELINE.md`).

## What we build, in priority order

1. **Stay light.** Every feature must keep the idle cost at zero
   (`PERFORMANCE_BUDGET.md`). When a feature and the budget conflict, the
   budget wins and the gap is documented.
2. **Behave like Docker where it can be done in user space:** networks with
   names, per-container addresses and a private localhost, restart policies,
   events, Compose stacks, volumes and port publishing.
3. **Say plainly what is different.** A container is not a kernel security
   boundary on stock Android. Every unsupported feature fails with a clear
   error and is listed in `COMPATIBILITY_MATRIX.md`.
4. **Manage it from anywhere you trust:** the Android screens, and an
   opt-in, paired, TLS-only Web Panel for a desktop browser on the home
   network.
5. **Serve:** an optional Server Mode that restores workloads when Android
   restarts the app, within Android's documented lifecycle.

## Non-goals

- Kernel-enforced isolation, cgroup resource limits, `--privileged`,
  devices, `pause`. These are impossible on stock unrooted Android.
- Defeating Android power management with permanent wake locks or
  persistence after a force-stop.
- A Portainer clone. The Web Panel is ThothDock's own small panel.
- Building images on the phone (BuildKit) in this programme. It is planned
  later, after the runtime work.
