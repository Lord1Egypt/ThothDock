# ThothDock architecture

```
cmd/thothdock            serve · version · doctor · inspect-store · gc
internal/api             Docker Engine API 1.41: router, version negotiation, errors,
                         containers, images, attach (hijack), logs (stdcopy framing)
internal/engine          container object model, persistence, lifecycle, wait conditions,
                         create-time policy (refuse what PRoot cannot honour), bind policy
internal/image           image store (images/<id>/image.json + rootfs/, refs.json) and pull
internal/registry        reference parsing (Docker grammar), HTTPS distribution client, bearer auth
internal/oci             media types, manifests/indexes/configs, digests, platform selection
internal/layer           layer application (whiteouts, opaque dirs) and rootfs copy
internal/securefs        chroot-semantics path resolution over directory file descriptors
internal/store           atomic files, content-addressed blob store
internal/logs            json-file logs, rotation, live chunk/entry subscriptions
internal/runtime         Runtime interface; PRootRuntime; FakeRuntime; PTYs
internal/platform        data-root layout; Android detection and trust store
```

Dependencies: the Go standard library and `golang.org/x/sys/unix` (for
`openat`-family calls, `readlinkat` and the PTY ioctls).

## Data root

```
<root>/                     0700
  run/thothdock.sock        0600 Unix socket (the API)
  run/thothdock.lock        flock: one daemon (or gc) per root
  engine-id                 stable daemon ID for `docker info`
  blobs/sha256/<hex>        manifests, configs, layers — only ever written after verification
  images/<id>/image.json    written last; a directory without it is an interrupted pull
  images/<id>/rootfs/       assembled image filesystem, never modified after the pull
  refs.json                 "docker.io/library/alpine:3.20" -> image ID (and name@digest)
  containers/<id>/config.json  the container record, written atomically
  containers/<id>/rootfs/      private copy of the image rootfs
  containers/<id>/{hosts,hostname,resolv.conf,container.log[.1]}
  tmp/                      staging; emptied when the daemon starts
```

Every metadata write uses write-temp, fsync, rename, then fsync of the
directory. Recovery on start:

- An image directory without `image.json` is removed.
- A container directory without `config.json` is removed.
- A container in `removing` state has its removal finished.
- A container recorded as running is reconciled with `/proc`. Its PID and
  start time must both match, so a reused PID is never mistaken for the
  container. A survivor is killed, and the container becomes `exited (137)`
  with an explanatory error.

## Pull pipeline

1. Parse the reference, then GET the manifest (Accept: OCI index, Docker list,
   OCI manifest, Docker manifest). The digest is computed from the bytes; it
   must equal a pinned digest and the `Docker-Content-Digest` header.
2. For an index, select the device platform (attestation entries never match).
   Fetch that manifest and check its size against the index.
3. Download the config and the layers into the blob store. Each is accepted
   only if its size and sha256 match, so a corrupt or oversized download never
   becomes visible.
4. Check the config's platform and diff-ID count.
5. Assemble in `tmp/rootfs-*`. For each layer: re-hash the stored blob, then
   stream it through gzip into `layer.Apply`, hashing the compressed bytes again
   and the uncompressed tar against the diff ID. Any mismatch or unsafe entry
   discards the staging directory.
6. Rename into `images/<id>/rootfs`, write `image.json`, update `refs.json`.

Shared layers are downloaded once. They are applied again for each image,
because every image has its own assembled rootfs.

## Containers

`create` validates the request and refuses features PRoot cannot honour (see
COMPATIBILITY.md). It then merges the image config (Env, Entrypoint/Cmd,
WorkingDir, User, Labels, StopSignal), copies the image rootfs, writes
`hosts`, `hostname` and `resolv.conf`, and finally the record.

`start`:

1. Resolve the user from the container's own `/etc/passwd` and `/etc/group`,
   and the executable through the container's `PATH`. Both lookups go through
   securefs.
2. Ask the runtime to start the process.
3. A monitor goroutine waits for it, delivers all output, records the exit, and
   wakes `wait` callers. With `--rm`, it then removes the container.

States are explicit (`created`, `starting`, `running`, `exited`, `failed`,
`removing`). The API maps them to Docker's vocabulary.

## Runtime

`runtime.Runtime.Start(Spec) → Process` with `Signal`, `Kill`, `Resize`,
`Stdin` and `Wait`. Docker's create/stop/exec/remove verbs are composed by the
engine from those primitives. `exec` will be a second `Start` with the same
rootfs, environment and binds.

PRootRuntime starts
`proot --rootfs --root-id|--change-id [--link2symlink] --cwd --kill-on-exit
--bind=/dev --bind=/proc --bind=/sys --bind=<hosts|hostname|resolv.conf> [binds] argv`:

- It runs in its own process group (or its own session with a controlling PTY
  for `-t`), with `Pdeathsig=SIGKILL` so containers die with the daemon.
- Signals go to the process group; `Kill` SIGKILLs PRoot, whose tracees die
  with it.
- PRoot's `terminated with signal N` line becomes exit code `128+N`.
- Startup refuses a PRoot without `--kill-on-exit`, `--root-id`,
  `--change-id` or `--link2symlink`.

## API

The router strips `/v<major>.<minor>`, refuses versions above 1.41 or below
1.24, and sets `Api-Version`, `Ostype`, `Docker-Experimental` and `Server` on
every response. Errors are `{"message": …}` with Docker's status codes
(400, 403, 404, 409, 304, and 501 for deliberately unsupported features).

- **attach** hijacks the connection. It answers `101 UPGRADED` when the client
  asked for an upgrade. It subscribes to the container's raw output before
  answering, so nothing is lost. It writes Docker's 8-byte stream frames, or
  raw bytes for a TTY. A client half-close (no more stdin) does not end the
  stream; the end of the run does.
- **wait** registers the waiter before flushing headers, so `docker run`
  never misses a fast exit.
