# Evidence index

| Path | What | Ticket |
|---|---|---|
| `phase0/baseline-v0.1.1-run{1,2,3}.txt` | v0.1.1 resource baseline, host | P0-03 |
| `phase1-3/ab-*.txt` | interleaved A/B after Phases 1–3 (and the fsync-variance investigation) | P0-04 |
| `final/{v0.1.1,dev}-run{1,2}.txt` | interleaved A/B of the complete build | P0-04 |
| `final/devnet-run{1,2}.txt` | four idle containers on a user network (Garden PRoot `--net-ip`) | P2-02, B-01/B-02 |
| `phase6/panel-*.png` | Web Panel in Chromium (desktop, logs dialog, 390 px phone width) | P6-03/04 |

Test outputs not stored as files are reproducible with the scripts named in
`../TEST_STRATEGY.md`, and run on every push in CI (`nextgen` job). Device
evidence: none yet in this programme (ADB unavailable on 2026-10-10).
