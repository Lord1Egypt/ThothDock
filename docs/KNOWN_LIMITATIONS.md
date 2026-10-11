# Known limitations

These are properties of running containers as ordinary Android processes under
PRoot. They are stated plainly; none is hidden behind a silent emulation.

| Area | Limitation |
|---|---|
| Security boundary | A container is a userspace environment, not a kernel security boundary. No PID, network, mount, IPC, UTS or user namespaces; no cgroups, capabilities, seccomp or LSM profiles. A compromised container process can reach whatever the hosting app can. |
| Published ports | `-p` is a user-space TCP forwarder. It binds `127.0.0.1` by default, and **Android loopback is device-wide**: any local app (and `adb shell`) can connect to a published port. Publish only what you would expose to every app on the phone. |
| Workload bind address | `-p` does not control the address the workload binds itself. A service listening on `0.0.0.0` inside the container is reachable on the device's network addresses whether or not it is published. |
| UDP | Not implemented. `-p …/udp` and `-P` fail with a clear error. |
| `docker exec` | An exec process runs in the container's root filesystem and environment but is not a member of a PID namespace: it is absent from the container's process table. |
| Metrics | No CPU or memory figures: there are no cgroups to read. `docker stats`, `top`, `pause` and `update` are unsupported rather than faked. |
| Resource limits | `--memory`, `--cpus`, `--pids-limit`, ulimits, devices, `--privileged` and capabilities are refused (HTTP 501), never ignored. |
| Lifetime | Containers do not outlive the app. Closing the last terminal window, Exit and force-stop end the daemon and every container. Running containers are reported `Exited (137)` after a kill. On the nextgen branch, restart policies bring eligible containers back when the engine starts again (the app is opened); nothing can restart them while Android keeps the app stopped. |
| Networks (nextgen) | A user-defined network is address translation in PRoot, not isolation: any container or app on the device can connect to a container's 127.77.x.y address; membership controls only which names resolve. Servers see peers as 127.0.0.1. There is no DNS server, so resolvers that bypass `/etc/hosts` do not see container names. |
| Volumes | No copy-up of image content into a new named volume; image `VOLUME`s do not become anonymous volumes. |
| Exit codes | A workload killed by a signal reports `128+signal` (a shell loop killed by SIGTERM reports 143; fixed since the Golden QA build, which reported 0). |
| Android process pressure | Android ends an app's child processes (and with them the engine and every container) when the device-wide count passes its cap (AOSP default 32; not readable by apps). Process Guard warns when the app's own count is high and reports recovery after an unclean exit; it cannot prevent the kill, never refuses a workload, and cannot see other apps' processes. About 22 processes (four small workloads) have run for hours; five more containers ended the engine. Use `--restart unless-stopped`. |
| Web Panel sessions | A paired browser stays authorised only while that panel process runs (and for 12 h without use). Stop, restart or a crash makes every browser pair again; "Disconnect all devices" does it without stopping the panel. |
| LAN Mode | ThothTerm's LAN terminal is plain HTTP on the local network: its PIN authenticates but nothing is encrypted. Use it only on a network you trust. |
| Isolation | The Garden/PRoot runtime separates workloads functionally; it has no Linux namespaces, cgroups or seccomp. Containers share the app's UID, so hostile native code in one container is not contained, and user-defined networks do not enforce network isolation. Do not run untrusted workloads. |
| Images | Pulled from registries over HTTPS only when you run `docker pull` / `docker run`; nothing is downloaded in the background. |
