# ThothDock security model

## The one-sentence version

**A ThothDock container is not a security boundary.** It is a private root
filesystem and process environment that PRoot translates in user space. It is
suited to development and portable tooling, not to running code you would not
run as your own Android app.

## What PRoot is and is not

PRoot intercepts a process's system calls with `ptrace` and rewrites paths, so
the process sees the container's root filesystem, binds and fake identity.
It provides:

- path translation (the rootfs, `--bind`);
- fake root (`--root-id`) and fake uid/gid (`--change-id`), so that package
  managers work. These grant **no** Android privilege;
- hard-link emulation (`--link2symlink`) where Android forbids real ones.

It does **not** provide namespaces (PID, network, mount, IPC, UTS, user),
cgroups (no memory, CPU or PID limits), capability dropping, seccomp, AppArmor
or SELinux profiles. A process that makes raw system calls the tracer does not
translate, or that leaves through a shared resource, can reach whatever the
ThothDock process can reach. That is the hosting Android app's sandbox, which
is the real boundary.

## Threat model

| | Threat | Stance | Mechanisms |
|---|---|---|---|
| **A** | Malicious OCI image content (registry, manifest, layers) | **Defended** | HTTPS only, redirects only to HTTPS; manifests and blobs size-bounded and sha256-checked before they enter the store; the platform manifest checked against the index's size; layers re-hashed before use and checked against the config's diff IDs while applied; unsafe layer entries (`..`, absolute paths, NUL, hard links outside the root or to directories, invalid whiteouts, unknown types) fail the whole pull; staging directories are discarded on any error; setuid, setgid and sticky bits are dropped; device nodes and FIFOs are skipped |
| **B** | Accidental interference between containers | **Partial** | Each container has a private copy of the image rootfs (two containers never share files; tested). They do share the device network, process table, `/dev`, `/proc` and `/sys` |
| **C** | Hostile local Android apps | **Defended where Android allows** | The data root is owner-only (0700) in app-private storage; the API socket is mode 0600 in an owner-only directory; nothing listens on TCP unless `--dev-tcp` is given, and that only accepts a loopback address and prints a warning (any app on the device can reach loopback, so it is for development only) |
| **D** | Hostile network clients | **Defended by default** | No network listener unless the user publishes a port; `-p` binds 127.0.0.1 unless `serve --allow-publish-nonlocal` is given; the Docker API is never exposed on TCP (`--dev-tcp` accepts loopback only and warns); UDP publishing is refused |
| **F** | Other apps on the same device reaching a published port | **Not defended** | Loopback is device-wide on Android: any app (and `adb shell`) can connect to `127.0.0.1:<hostport>`. Publish only services you would expose to every app on the phone. The API socket, unlike a published port, is owner-only: the `shell` uid and other apps get `EACCES` (device-verified) |
| **G** | A container being reachable more widely than its `-p` mapping says | **Documented gap** | ThothDock forwards to `127.0.0.1:<container port>` but cannot control which address the workload itself binds. A service listening on `0.0.0.0` is reachable on the device's LAN address whether or not it is published |
| **H** | Apt or a user replacing ThothDock with a real engine in the Debian/Ubuntu guest | **Defended (Engine Guard)** | Placeholder packages at epoch 9999, an apt pin of priority 1001, and a dpkg pre-install hook that refuses real `docker.io`/`docker-ce`/`containerd`/`runc` builds. Guard is a safety rail for the user's own mistakes, not a defence against a hostile root in the guest |
| **I** | Volume names or mounts used to escape the data root | **Defended** | Names follow Docker's pattern; data lives in `volumes/<name>/_data`; removal never follows symlinks; binds still need `--allow-bind` and canonicalisation; volumes in use cannot be removed |
| **E** | A compromised container process | **Not defended** | It runs as the app's uid under ptrace translation and can reach everything the app can, including ThothDock's own data root through raw system calls. Do not run untrusted images with secrets in the same app |

## Extraction rules (threat A, in detail)

`internal/securefs` resolves every path inside a root with chroot semantics.
Each component is opened relative to a directory file descriptor with
`O_NOFOLLOW`, symlinks are read and re-resolved against the root (absolute
ones from the root, `..` clamped at the root, at most 40 links), and nothing
is resolved through the host's view of the path. The final component of
every write is never followed: an existing non-directory is unlinked, then
created with `O_CREAT|O_EXCL|O_NOFOLLOW`. Whiteouts (`.wh.<name>`) and opaque
markers (`.wh..wh..opq`) remove only what lies inside the same root, and never
an entry the same layer created. Every one of these rules has a test,
including mutation checks showing the tests fail when the rule is removed
(`internal/layer/apply_test.go`, `internal/securefs/securefs_test.go`,
`internal/image/image_test.go`).

## Resource ceilings on pull (threat A, storage exhaustion)

A pull is bounded by explicit ceilings, set with `thothdock serve` options.
A value of `0` or the option left out takes the default; `-1` disables that
one limit. Every ceiling counts bytes actually read and written, not what a
manifest claims, and a pull that crosses one fails with an error naming the
option and discards the staging directory (nothing partial becomes an image).

| Option | Default | What it bounds |
|---|---|---|
| `--max-layers` | 128 | layers per image, checked from the manifest before any download |
| `--max-compressed-bytes` | 8 GiB | sum of the layer sizes the manifest declares, checked before any download; each blob is also held to its declared size while it downloads |
| `--max-layer-bytes` | 16 GiB | one layer's uncompressed tar stream (the decompression-bomb ceiling), headers and padding included |
| `--max-extracted-bytes` | 16 GiB | bytes written for the whole image: file contents plus hard links the platform forces into copies. It counts bytes written, not net size, so a file a later layer deletes still counts |
| `--max-entries` | 4,000,000 | tar entries (files, directories, links) for the whole image |
| `--min-free-bytes` | 512 MiB | free space that must remain on the store's filesystem: checked before each layer download (the layer's size must also fit), before extraction, and about every 32 MiB written |

Free space comes from `statfs` of the store's filesystem. Where the platform
does not report it, only the ceilings above apply; the check never blocks a
pull merely because the figure is unavailable. The defaults sit far above
ordinary images (large CUDA or PyTorch bases are single-digit GiB). What the
ceilings do **not** do: they do not bound the space containers themselves
write at run time (there are no cgroups or quotas, see above), and a layer
blob that failed extraction stays in the blob store, verified, until `gc`.

## API rules

- Request bodies are limited to 1 MiB and decoded into typed structures
  (unknown fields are ignored, as dockerd ignores them).
- Container names follow Docker's pattern; IDs are 64 random hex digits; image
  references are parsed with Docker's grammar.
- Bind mounts are refused unless the daemon was started with `--allow-bind
  DIR`. A bind source is resolved to its canonical path and must lie inside an
  allowed directory; ThothDock's own data root, and any directory containing
  it, can never be bound. Read-only binds are refused, not silently made
  writable, because PRoot cannot enforce read-only binds.
- Features that would need kernel support (`--privileged`, resource limits,
  capabilities, devices, seccomp/AppArmor options, read-only rootfs, tmpfs,
  sysctls, `--network none`) return HTTP 501 with an explanation. They are
  never accepted and ignored.
- `docker exec` runs in the container's own root filesystem with the container's environment; `--privileged` is refused, and the user/workdir/env of an exec are validated like a create.
- Registry credentials from `X-Registry-Auth` are used for that pull only and
  are never logged or stored.

## Logs

Container output is stored per container (`container.log`, json-file
format), rotated at 10 MiB with one previous file kept, and deleted with the
container. The daemon logs requests only at `--debug` and never logs request
bodies or credentials.
