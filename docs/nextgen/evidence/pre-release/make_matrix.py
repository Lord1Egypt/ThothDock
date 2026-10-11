#!/usr/bin/env python3
"""Writes test-matrix.json and test-matrix.csv (the QA closure matrix) next to this file.

Every row is a test that was actually run, or is marked PENDING/SKIP/BLOCKED with the reason.
Environments: host (automated, this computer), android-unit (Gradle unit tests),
device (SM-A165F, Android 16, QA package com.thothterm.debian.qa.nextgen), owner (owner-assisted).
"""
import csv
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ENGINE = "ThothDock be197c5"
APP = "AndroidThothTerm 7985e48 (pins ThothDock be197c5, PRoot patch 0009 c6c15b6)"
E = "docs/nextgen/evidence/pre-release/"

rows = [
    # id, feature, env, expected, actual, status, evidence, commit
    ("A-01", "Device and baseline facts", "device", "SM-A165F, Android 16, arm64, Debian 13, ThothDock 0.1.1", "confirmed from adb and the guest", "PASS", E + "state/", APP),
    ("A-02", "Pre/post-test container state recorded", "device", "configs saved before changes", "4 owner containers' inspect saved; all 4 running at the end", "PASS", E + "state/pre-inspect-containers.json", APP),
    ("B-01", "In-place tabs: Containers > Images > Volumes > Web Panel > Containers", "device", "no new Activity, correct highlight", "task size stays 2, one chip selected each step, 4 containers running", "PASS", E + "tabs/", APP),
    ("B-02", "Back navigation", "device", "one Back leaves the screen; no stacked screens", "one Back -> Terminal (task size 1); drawer entries reuse the screen", "PASS", E + "tabs/", APP),
    ("B-03", "Recreation (rotation) and background/foreground", "device", "tab and panel state survive, no crash", "Web Panel tab kept through landscape and Settings round trip; 0 crashes", "PASS", E + "tabs/7-landscape.png", APP),
    ("B-04", "Scroll-state restoration", "device", "per-tab scroll restored", "implemented (saved per tab, restored on recreation); not measured on the device", "PARTIAL", "garden-debian ContainersActivity", APP),
    ("B-05", "Tab stress: 120 switches", "device", "no growth in activities, views, memory or processes", "Activities 2, ViewRootImpl 2, PSS 142->131 MB, engine threads 17, processes unchanged", "PASS", E + "state/tabs-stress-after.txt", APP),
    ("B-06", "Web Panel lifecycle independent of the tab", "device", "server keeps running across tab switches; off by default", "same panel pid through every switch; no panel process after a fresh install", "PASS", E + "tabs/", APP),
    ("C-01", "LAN Mode off by default; transport warning", "device", "Off; says traffic is unencrypted", "Status: Off; warning shown", "PASS", E + "lan/lan-1-off.png", APP),
    ("C-02", "LAN pairing, terminal I/O, Debian guest", "device", "PIN pairs; typing and output work", "paired; echo round trip; Debian guest", "PASS", E + "lan/lan-pair.log", APP),
    ("C-03", "LAN terminal resize", "device", "guest size follows the browser", "40x135 -> 25x93", "PASS", E + "lan/lan-pair.log", APP),
    ("C-04", "LAN file and folder upload", "device", "files arrive intact; nothing overwritten", "2 files + folder, SHA-256 identical; existing names kept as '(n)'", "PASS", E + "lan/lan-pair.log", APP),
    ("C-05", "LAN reconnect, sign-out", "device", "new tab reconnects without PIN; sign-out kills the token", "reconnected; old token 401", "PASS", E + "lan/lan-pair.log", APP),
    ("C-06", "LAN Host/Origin checks", "device", "foreign Host and Origin refused", "421 / 403", "PASS", E + "lan/lan-pair.log", APP),
    ("C-07", "LAN PIN single use, lockout, expiry", "device", "used PIN dead; locked after 5; dead after 2 min", "'Used'; 423 locked; 410 expired at 129 s", "PASS", E + "lan/", APP),
    ("C-08", "LAN Mode and Web Panel coexistence", "device", "neither credential opens the other", "LAN token 401 on panel; panel session 401 on LAN", "PASS", E + "lan/lan-coexist.log", APP),
    ("C-09", "LAN and panel Stop", "device", "listeners closed, no orphans", "both ports closed; panel process gone; no extra shells", "PASS", E + "lan/lan-4-stopped.png", APP),
    ("D-01", "Web Panel loopback mode and certificate", "device", "LAN address unreachable; served cert = fingerprint in app", "Wi-Fi address not reachable; fingerprint matches", "PASS", E + "sec-session.log", APP),
    ("D-02", "Web Panel unauthenticated, CSRF, Origin, Host, cookie flags, allow list, sign-out", "device", "refused without session/CSRF/Origin/Host; HttpOnly Secure Strict", "20/20", "PASS", E + "sec-session.log", APP),
    ("D-03", "Web Panel code rate limit and exhaustion", "device", "429 within 1 s; dead after 5 wrong", "429; right code refused afterwards; app shows 'No active code'", "PASS", E + "sec-exhaust.log", APP),
    ("D-04", "Web Panel Wi-Fi mode, HTTP->HTTPS, browser flow", "device", "HTTPS 200 on Wi-Fi, http redirected, panel functions", "200; 307 to https; device browser e2e 12/12", "PASS", E + "tabs/9-webpanel-wifi.png", APP),
    ("D-05", "Pairing-code expiry (10 min) and session idle expiry (30 days)", "host", "expired code and idle session refused", "unit tests with a fake clock; not waited out on the device", "PASS", "internal/panel panel_test.go", ENGINE),
    ("D-06", "Web Panel browser e2e (functional, CSP, phone width)", "host", "30 checks", "30/30", "PASS", "tests/panel/e2e.sh", ENGINE),
    ("E-01", "TON stack failure root cause", "device", "classify DNS/TCP/TLS/HTTP/app", "DNS: getaddrinfo EAI_AGAIN only on user networks; PRoot --net-ip moved bind(0.0.0.0:0) to 127.77.x.y -> EINVAL; fixed in patch 0009", "PASS", "docs/nextgen/QA-CLOSURE-2026-10-10.md", APP),
    ("E-02", "TON stack: config, pull, up -d, ps, logs, restart, down", "device", "2/2 running, scoped cleanup", "project tonqa 2/2; down removed only its containers and network", "PASS", "docs/nextgen/QA-CLOSURE-2026-10-10.md", APP),
    ("E-03", "TON API /block/latest, explorer, WebSockets", "device", "valid JSON, HTTP 200, WS data", "seqno JSON; 200 (62 KB); /block/watch and /block/watch/changed 101 + data", "PASS", "docs/nextgen/QA-CLOSURE-2026-10-10.md", APP),
    ("E-04", "Deterministic Compose fixture in CI", "host", "two-service stack passes", "compose.sh 26/26", "PASS", "tests/regression/compose.sh", ENGINE),
    ("F-01", "Network semantics on the phone", "device", "built-ins truthful, none refused, names, aliases, -p, membership, outbound DNS/HTTPS, prune", "26/26", "PASS", E + "networks-device.log", APP),
    ("F-02", "Networking regression (host)", "host", "all checks incl. outbound probe", "51/51; probe fails on the old PRoot, passes on the fixed one", "PASS", "tests/regression/networks.sh", ENGINE),
    ("F-03", "Logical membership vs isolation", "device", "documented limitation measured", "another network reaches a container by address (KNOWN LIMITATION)", "PASS", E + "networks-device.log", APP),
    ("G-01", "Restart policies, crash restart, backoff, events, liveness", "device", "Docker semantics", "16/16", "PASS", E + "restart-device.log", APP),
    ("G-02", "Restart-policy regression with SIGKILLed daemon", "host", "Docker semantics after daemon kill", "21/21", "PASS", "tests/regression/restart-policies.sh", ENGINE),
    ("G-03", "Force-stop and recovery", "device", "everything stops; restored on next launch", "0 processes after force-stop; 4 containers back 4 s after launch", "PASS", "docs/nextgen/ANDROID_LIFECYCLE.md", APP),
    ("G-04", "Screen off", "device", "workloads keep serving", "screen off 04:11-04:18 UTC during the 1 h soak: engine, API and explorer kept serving (only 7 minutes; a long screen-off run belongs to the 6 h soak)", "PASS", E + "soak/", APP),
    ("G-05", "Network disconnection", "device", "recover after Wi-Fi loss", "not run: ADB reaches the phone over Wi-Fi, so cutting Wi-Fi ends the test session", "SKIP", "", APP),
    ("H-01", "Engine Guard host suite", "host", "115 checks", "115/115", "PASS", "engine-guard/test.sh", ENGINE),
    ("H-02", "Engine Guard on the phone", "device", "48 checks; doctor --guard clean", "48/48; 0 WARN/FAIL", "PASS", E + "guard-hardening-final.log", APP),
    ("I-01", "Idle engine cost with the four workloads", "device", "no material regression", "10.9 MB RSS, 16 threads, 70 ms CPU and 279 switches per minute (TON API logs every second)", "PASS", "docs/nextgen/QA-CLOSURE-2026-10-10.md", APP),
    ("I-02", "Container startup and exec latency", "device", "sub-second to ~1 s", "run: 0.6-1.3 s; on a user network 0.6-1.0 s; exec 0.57 s", "PASS", "docs/nextgen/QA-CLOSURE-2026-10-10.md", APP),
    ("I-03", "Host perf comparison against the NextGen baseline", "host", "no material regression", "idle 0/0, RSS +-1%, startup -8%", "PASS", "tests/perf/compare.py", ENGINE),
    ("I-04", "Phantom-process budget", "device", "stay under the cap", "owner workload = 22 processes; +5 test containers got the engine killed (Trimming phantom processes)", "FAIL", E + "phantom/", APP),
    ("I-05", "1 h soak, four workloads, screen off", "device", "no restarts, API/explorer answer, no kills", "61/61 samples: one engine pid, 4 running, API answered 61/61 (8,722 blocks, in order), explorer 200 61/61, 0 phantom kills, RSS 11.6->6.6 MB, 16 threads, 28.8-29.1 C", "PASS", E + "soak/soak-1h-2026-10-11.csv", APP),
    ("I-06", "6 h / 24 h / 48 h soak", "device", "real elapsed time", "not run in this session", "PENDING", "tests/device/soak.sh", APP),
    ("I-07", "Gaming coexistence (Wild Rift)", "owner", "owner plays with 4 containers running", "not done in this session", "PENDING", "", APP),
    ("I-08", "Battery and thermal", "device", "observe, not a power measurement", "the phone was charging; temperature recorded in the soak only", "PARTIAL", E + "soak/", APP),
    ("J-01", "Event stream ordering, pulls, networks", "device", "create/start/die/destroy order; pull; network events", "recorded in the restart test", "PASS", E + "restart-device.log", APP),
    ("J-02", "Bounded history, slow clients, reconnect", "host", "ring of 1024, live-only without since, reconnect", "go test -race (events, api, panel)", "PASS", "internal/events, internal/panel", ENGINE),
    ("K-01", "Go tests, race detector, vet", "host", "all pass", "all packages ok", "PASS", "go test -race ./...", ENGINE),
    ("K-02", "Android unit tests", "android-unit", "all pass", "garden-common 375/375, garden-debian 33/33", "PASS", "Gradle test results", APP),
    ("K-03", "Docker CLI smoke and lifecycle", "host", "all pass", "smoke 6/6, lifecycle 12/12", "PASS", "tests/smoke, tests/regression/lifecycle.sh", ENGINE),
    ("K-04", "Reproducibility preflight (guard packages under umask 022/002)", "host", "identical", "identical (Engine Guard round)", "PASS", "engine-guard/build.sh", ENGINE),
]

fields = ["test_id", "feature", "environment", "expected", "actual", "status", "evidence", "commit"]
data = [dict(zip(fields, r)) for r in rows]
with open(os.path.join(HERE, "test-matrix.json"), "w") as f:
    json.dump({"generated_by": "make_matrix.py", "engine": ENGINE, "app": APP, "tests": data}, f, indent=1, ensure_ascii=False)
    f.write("\n")
with open(os.path.join(HERE, "test-matrix.csv"), "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=fields)
    w.writeheader()
    w.writerows(data)
counts = {}
for r in data:
    counts[r["status"]] = counts.get(r["status"], 0) + 1
print(json.dumps(counts))
