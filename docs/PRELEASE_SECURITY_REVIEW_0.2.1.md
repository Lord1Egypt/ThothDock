# Pre-release security review: Garden PRoot patches 0010–0018 and ThothDock 0.2.1

Scope: the changes of this round, reviewed in source and exercised in
`tests/garden-common/proot/*-check.sh` (Garden) and the compatibility suite.
Threat model: ThothDock gives no kernel isolation. A container's workload runs
as the application's uid; the question is whether a workload can gain
something it did not have, or reach another container's resources through the
new code.

| Patch | Change | Result |
|---|---|---|
| 0010 | System V IPC kept inside PRoot (memfd segments, one-shot socket) | One finding fixed (below); no residual issue |
| 0011 | `setgroups`, `capset`, bounding/ambient `prctl` follow the fake-root capability model | Reviewed, no change needed |
| 0012 | SIGQUIT passed to the guest | Reviewed, no issue |
| 0013 | `--hostname` rewrites `uname`'s node name | Reviewed, no issue |
| 0014 | Long Unix socket paths kept at their real path | **Finding fixed**: collidable alias names |
| 0015 | systemd-resolved stub addresses are not redirected | Reviewed, no issue |
| 0016, 0017 | Guest `chroot()` as fake root no longer aborts PRoot | Reviewed; fixes memory-safety bugs (below) |
| ThothDock | Stop signals also reach a workload that left the process group | Reviewed, no issue |

## Finding 1 (fixed): predictable alias names in 0014

The first version named the short alias of a long socket path
`prootsock-<FNV-1a(host path)>`. FNV-1a is not collision resistant. A guest
that chooses its own socket path (it controls the path text inside its own
rootfs) could craft a path whose hash equals another container's socket path;
its bind would replace that alias, and the other container's `connect` could
then follow the alias to the attacker's socket (a connect redirected to
another container's socket).

Fix: every name is 64 random bits from `getrandom(2)` (fallback
`/dev/urandom`), created by `symlink(2)` or `bind(2)`, which fail rather than
reuse a name. `bind` uses `prootsock-b-<hex>` (left as a symlink to the socket's
real path), `connect` a private `prootsock-c-<hex>` removed at the syscall's
exit. Names are not derivable from anything the guest controls.
Tests (`guest-semantics-check.sh`): names are distinct and unguessable, a
connect leaves no alias, dead aliases are swept.

Residual, accepted: a tracee killed between a connect's entry and exit leaves
one symlink to a socket that still exists. It carries no authority (it points
where the connect already pointed) and costs one directory entry.

Other properties checked for 0014:

- Symlink and TOCTOU: the directory is `PROOT_TMP_DIR`, created `0700` by
  ThothDock (`NewPRoot`), owned by the application and not mounted into any
  container. Guest processes cannot create, list or replace entries there.
  Creation uses operations that refuse existing names; the remaining
  `rename`/`symlink` after a bind act on names only this PRoot holds.
- Target confinement: the alias target is the *translated* host path of the
  guest's own path, resolved by PRoot inside the container's rootfs
  (guest symlinks, `..` and absolute links stay inside it). A guest cannot
  point an alias outside its rootfs.
- `bind` over an existing path returns `EADDRINUSE`, as the kernel does; the
  existing socket is untouched.
- No new thread, helper process or polling: the only added ptrace stop is at
  the exit of a *long-path* `bind`/`connect`.

## Finding 2 (fixed): `mktemp` in the shared-memory listener (0010)

0010's shmat listener and upstream's `--shm-helper` generated the socket name
with `mktemp(3)` (the compiler warned during development). The name was never
a security boundary (`bind` refuses an existing name and the receiver checks
`SO_PEERCRED` against the tracee's thread group) but the pattern is fragile.
They now use the same random helper; the final binary imports no `mktemp`,
`tmpnam` or `tempnam`. The first conversion used 128 random bits and made the
path exceed `sun_path` in a deep `PROOT_TMP_DIR` (`shmat` failed with
`ENOMEM`; caught by re-running `sysvipc-check.sh`); 64 bits fit and remain
unguessable.

0010 properties: segments are `memfd` descriptors owned by PRoot, closed with
it (no `/dev/shm`, no persistent object, nothing survives the container);
`shmat` hands a descriptor only to a connection whose peer credentials name
the tracee's own thread group; one IPC namespace per PRoot, so one container's
segments are invisible to another's (`P-IPC` checks the same key created in two
containers at once). No false claim of IPC namespace isolation: there is none
against a hostile process that can ptrace or open `/proc/<pid>/fd` of another
container's PRoot, as with every ThothDock container (same uid).

## Capability emulation (0011)

Only the *result* of a refused call is changed. The kernel still runs the call
as the unprivileged application: `setgroups`, `capset`, `PR_CAPBSET_DROP` and
`PR_CAP_AMBIENT` fail with `EPERM` there, and the patch turns that single
errno into success when fake_id0's own model says the process holds the
capability (`euid == 0` or `caps_active`, the flag already used for
`setuid`/`setgid`, cleared on a permanent UID drop unless `PR_SET_KEEPCAPS`).
`EINVAL` and every other error pass through. Nothing is granted: group
membership and capabilities are not emulated and the kernel credentials never
change. A process that dropped to an unprivileged uid without `KEEPCAPS` still
gets `EPERM` (tested). `capset` moved from the shared "euid == 0 only" case to
the same model; for a fake root the behaviour is identical to before.

## SIGQUIT (0012)

PRoot stopped treating SIGQUIT as a crash of its own. SIGKILL, SIGHUP and
SIGTERM handling is unchanged; a container that ignores SIGQUIT can still be
ended by `docker stop` (SIGKILL after the timeout) and `docker kill`. The
crash signals (SIGILL, SIGABRT, SIGFPE, SIGSEGV) still kill all tracees.

## Host name (0013)

Only the `nodename` field of `uname`'s result is rewritten, after a
successful call, with a name ThothDock validates (`validHostname`) and PRoot
re-validates (1–64 bytes, no `/`). No other field, no `sethostname`. It
changes what the guest *reads*; nothing is written to the host.

## DNS (0015)

`connect`/`sendto` to exactly `127.0.0.53` and `127.0.0.54` are no longer
rewritten by `--net-ip`, so a container whose `resolv.conf` was copied from a
systemd-resolved host reaches that host's resolver. Nothing is redirected to
any other server, public or private; no fallback to 8.8.8.8 was added (the
existing Docker-style fallback only applies when the host file has no
nameserver at all, and warns). A container service bound to those two
addresses inside its own network is the only thing that changes. Phones have
no such stub.

## chroot (0016, 0017)

Both are bug fixes in code that already existed (Termux's fake-root `chroot`
emulation), found by running sshd. 0016: the bindings destructor looked its
tracee up through a namespace that was already detached (talloc type abort)
and unlinked from `tracee->fs`, the *new* namespace, which is a use of the
wrong object; it now uses the list's own namespace. 0017: a stack buffer
(`path_guest`) was compared before being initialised, and a failed
`realpath` was ignored; the guest path is now always read before use and a
failure returns its error. Both removed crashes (a guest could abort PRoot
and with it every process of its own container; it could not affect other
containers' PRoot). The emulation's authority is unchanged: it only swaps
PRoot's *view* of the root for this tracee, for a process that is fake root,
into a directory it could already name; real credentials stay the
application's, and bind mounts below the new root are still refused
("unsupported chroot variant"). The change does not weaken or widen path
translation: `chroot` into a missing directory fails cleanly (tested).

## Signals to a workload outside the group (ThothDock)

`Signal()` now also sends the signal to the container's main process when
that process left proot's group. The pid is found as the single process whose
parent is the proot pid, under the lock that guarantees proot is still alive
and unreaped, so the pid cannot have been reused by another process. Only
the signal the user asked for (`docker stop`/`kill`/`restart`) is sent, and
only to a process that is a direct child of this container's own proot.

## ThothDock engine changes

- `--sysvipc`/`PROOT_DONT_SHARE_LIBANDROID_SHMEM`: set by the engine, not by
  the container (a container's own `PROOT_*` variables are stripped).
- `--hostname`: validated twice.
- Anonymous-volume warning: diagnostic only.

## Resource and background-activity check

No new thread, helper process, timer or polling loop in any patch. IPC and
alias work happens only inside the call that needs it. Measured on a Linux
host: idle CPU 0 ms/60 s and 0 context switches with four containers for both
the baseline and the candidate; 19 processes in both; engine RSS and thread
count equal within noise (`stageF-*` evidence in the round report).

## Not covered

The Android gate has since been run (see the round report): the socket
aliases work under the app's SELinux domain, and the run found one more
device-only issue, patch 0018 (`PR_SET_KEEPCAPS` for fake root). It only turns
a kernel `EPERM` caused by Android's locked securebits into success when the
fake-id0 model already treats the process as root; no credentials change.
