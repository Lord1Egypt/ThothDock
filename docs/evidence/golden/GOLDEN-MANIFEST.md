# ThothDock Golden QA — frozen rollback manifest (2026-10-05)

This snapshot is immutable. Nothing listed here may be rewritten, deleted,
force-pushed, re-tagged or replaced. If a later release goes wrong, return to
exactly these refs and this APK.

## Source

| | ThothDock | AndroidThothTerm |
|---|---|---|
| Repository | `Lord1Egypt/ThothDock` | `Lord1Egypt/AndroidThothTerm` |
| Annotated tag | `thothdock-golden-20261005` | `trixie-thothdock-golden-20261005` |
| Tag target (source of the APK) | `5a125f447ffd697c20610d4055b151256f76a09c` | `f20a0956dd56b4c0970dbcb3fe8da302a6a735ad` |
| Archive branch | `archive/thothdock-golden-20261005` @ `5a125f4` | `archive/thothdock-golden-20261005` @ `40c376afba0e601038d235b40ab1def4e84d3f06` |
| Working branch | `feature/golden` @ `5a125f4` | `feature/thothdock-golden` @ `40c376a` |

Notes on provenance:

- The Golden APK was built from AndroidThothTerm **`f20a095`**. `40c376a` is the
  later evidence/docs branch head; it did **not** produce the APK. It is kept
  on the archive branch only so the review history is preserved.
- The ThothDock Go sources inside the APK are `09076cc`; `5a125f4` only adds
  docs and evidence after that.

## Golden APK

| | |
|---|---|
| File | `ThothTerm-ThothDock-Golden-QA.apk` |
| Size | 93190419 bytes |
| SHA-256 | `a467ada2a2399e7ea0f1fab274948c66665a9c0db5d577161fbeb46cec63d9c4` |
| Package ID | `com.thothterm.debian.qa.thothdock` |
| versionName | `0.2.2-thothdock-golden.1` |
| versionCode | `202901` |
| Signing | APK Signature Scheme v2, Android **Debug** certificate (`C=US, O=Android, CN=Android Debug`) |
| Certificate SHA-256 | `15cf75f9945d5354e75707e0326b7cffc60ac51a68df38156db318ef4578a27c` |

QA / rollback snapshot only. Debug-signed, debuggable, not for production update.

## Device acceptance

| | |
|---|---|
| Device | Samsung SM-A165F, Android 16, unrooted |
| CLI acceptance | 63 / 63 PASS (Docker CLI 29.8.1, stock) |
| Go tests | 99 passed with `-race` against a real PRoot, 15 packages |
| Evidence path | `docs/evidence/golden/` in ThothDock at tag `thothdock-golden-20261005` (`README.md`, `device-acceptance.log`, `device-lifecycle.log`, `go-test-summary.txt`, `production-apks-before.txt`, `production-apks-after.txt`, `screenshots/`) |

## Golden feature set

Docker Engine API 1.41; Docker CLI compatibility; Engine Guard; `docker exec`;
named volumes; TCP localhost port publishing (UDP refused); Containers screen;
lifecycle recovery; Garden PRoot runtime. No dockerd, no containerd, no runc.

## Known limitations at Golden

A SIGTERM'd shell loop can report 0 instead of 143; no copy-up into new named
volumes; no UDP publishing; the workload's own bind address is not controlled;
exec processes are not in the container's PID view; containers do not survive
the app; Android loopback is device-wide, so a published port is reachable from
any local app. Containers are userspace environments, not a kernel security
boundary.

## Reproducibility

Two clean builds have identical contents but different bytes (the Android
Gradle plugin orders `classesN.dex` differently between runs). The APK is **not**
claimed reproducible.
