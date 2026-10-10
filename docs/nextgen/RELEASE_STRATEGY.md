# Release strategy

1. **Branches.** Engine work on ThothDock `feature/nextgen`; Android work on
   AndroidThothTerm `feature/thothdock-nextgen` (from `master` 6959201).
   Nothing is merged, tagged or released without the owner.
2. **Untouched while this runs:** published tags (`v0.1.1`,
   `trixie-v0.3.1`, Golden refs), GitHub releases, the F-Droid MRs
   (!50342 and the others), production signing keys, and
   `com.thothterm.debian` on the owner's phone.
3. **QA builds** are debug-signed, use the isolated application id
   `com.thothterm.debian.qa.nextgen`, and are built from pushed commits only
   (the ThothDock submodule and `thothdock.properties` pin the same commit).
4. **Release candidate gate** (all must hold, with evidence files):
   - every Phase 0–4 and 6 exit criterion in `phases/` met, including the
     device rows now PENDING;
   - `PERFORMANCE_BUDGET.md` gates pass on the host **and** on the device;
   - CI green (lint, race tests, builds, smoke, nextgen);
   - the owner's real stack (TON API + Explorer) runs from a Compose file on
     the QA build for at least 24 h with an unchanged idle profile;
   - reproducibility of the unsigned APK re-checked (patch 0009 changes
     `libproot.so` for every edition; the F-Droid recipes would need a new
     version entry, never an edit of a published one).
5. **Release** only after explicit owner approval: version bump (engine
   v0.2.0, Trixie edition 0.4.0 proposed), tags superseding — never moving —
   the old ones, owner-run production signing, then the F-Droid update.
6. **Rollback.** v0.1.1 reads records written by the new engine, but it
   validates host configuration only at create and ignores fields it does
   not know: an existing container with a restart policy starts and is
   never restarted, and a container on a user-defined network starts on the
   device network, where its hosts file names 127.77.x.y peers that no
   longer listen there. `networks.json` is ignored. Before rolling back,
   remove user-network containers (or `docker network disconnect` them) and
   expect no policy restarts.
