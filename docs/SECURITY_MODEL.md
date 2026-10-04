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
| **D** | Hostile network clients | **Defended** | No network listener by default; `-p` publishing is not implemented; when it is, it will bind 127.0.0.1 unless LAN exposure is explicitly requested |
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
- Registry credentials from `X-Registry-Auth` are used for that pull only and
  are never logged or stored.

## Logs

Container output is stored per container (`container.log`, json-file
format), rotated at 10 MiB with one previous file kept, and deleted with the
container. The daemon logs requests only at `--debug` and never logs request
bodies or credentials.
