# ThothDock 0.1.1 validation (2026-10-07)

Host: Linux x86-64 (WSL2), Go 1.27.1, host PRoot built from termux/proot at
Garden's pin (talloc from Garden's vendored copy), Docker CLI 29.8.1, apt 2.8.3.
No dockerd, containerd or runc anywhere.

| Check | Result | Log |
|---|---|---|
| gofmt, go vet | clean | static.log |
| go test ./... | exit 0 | go-test.log |
| go test -race ./... (real PRoot, no skips) | 113 top-level tests PASS, 0 FAIL, 0 SKIP | go-test-race-v.log |
| Engine Guard hook: recorded + real apt/dpkg scenarios | all passed | engine-guard-test.log |
| Docker CLI smoke with real Debian trixie guest (pull, run, exec, volumes, -p, Engine Guard: apt full-upgrade, stock install refused) | all passed | host-smoke.log |
| Lifecycle regression (run, logs, stop, start, restart, kill, wait, rm, 127, interactive TTY, pull ceiling) | all passed | host-lifecycle.log |
| OCI hostile layer/securefs tests | part of the race run above | go-test-race-v.log |

Issue fixes and their proofs:

* #3 engine-guard/test.sh: the old parser fails 8 of its checks, the new one passes all; fixtures under engine-guard/testdata were recorded from real apt 2.8.3 output.
* #4 internal/runtime/proot_test.go (TestSignalAfterExitIsRefusedAndHarmless, TestGroupLeftoversAreRemovedWhenLeaderExits, TestSignalRacesWithExit) and internal/procid/procid_test.go (KillGroup), under -race.
* #5 internal/image/limits_test.go; the limits are documented in docs/SECURITY_MODEL.md.
* #6 is the Android side (AndroidThothTerm 0.3.1): the hook is bundled and written into the guest before it is ready.
