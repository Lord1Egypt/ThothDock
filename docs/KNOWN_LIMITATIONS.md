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
| Lifetime | Containers do not outlive the app. Closing the last terminal window, Exit and force-stop end the daemon and every container. Running containers are reported `Exited (137)` after a kill. |
| Volumes | No copy-up of image content into a new named volume; image `VOLUME`s do not become anonymous volumes. |
| Exit codes | A workload killed by a signal reports `128+signal` (a shell loop killed by SIGTERM reports 143; fixed since the Golden QA build, which reported 0). |
| Images | Pulled from registries over HTTPS only when you run `docker pull` / `docker run`; nothing is downloaded in the background. |
