#!/usr/bin/env python3
"""Merge compatibility runs into the matrix (stdlib only).

    runner/report.py OUT_DIR LABEL=EVIDENCE_DIR...

LABEL names a column, e.g. host-baseline, host-candidate, android-candidate;
EVIDENCE_DIR holds results.txt, environment.txt and <ID>.log from run.sh.
Writes OUT_DIR/matrix-results.json, matrix.csv and summary.md. A test that
a run did not include is NOT_TESTED in that column; nothing is inferred.
"""
import csv, json, os, re, sys

here = os.path.dirname(os.path.abspath(__file__))
matrix = json.load(open(os.path.join(here, "..", "matrix.json")))
out = sys.argv[1]
runs = []
for arg in sys.argv[2:]:
    label, path = arg.split("=", 1)
    env = {}
    if os.path.exists(os.path.join(path, "environment.txt")):
        for line in open(os.path.join(path, "environment.txt")):
            k, _, v = line.strip().partition("=")
            env[k] = v
    res = {}
    for line in open(os.path.join(path, "results.txt")):
        kv = dict(re.findall(r"(\w+)=(\S+)", line))
        res[kv["id"]] = kv
    runs.append((label, path, env, res))

def first_failure(path, tid):
    log = os.path.join(path, tid + ".log")
    if not os.path.exists(log):
        return ""
    for line in open(log, errors="replace"):
        if line.startswith(("FAIL ", "BLOCKED ")):
            return line.strip()
    return ""

rows = []
for t in matrix["tests"]:
    row = dict(t)
    for label, path, env, res in runs:
        r = res.get(t["id"])
        row[label] = r["status"] if r else "NOT_TESTED"
        row[label + "_checks"] = f"{r['pass']}/{int(r['pass']) + int(r['fail'])}" if r else ""
        row[label + "_seconds"] = r["seconds"] if r else ""
        row[label + "_first_failure"] = first_failure(path, t["id"]) if r and r["status"] != "PASS" else ""
        row[label + "_evidence"] = os.path.join(path, t["id"] + ".log") if r else ""
    rows.append(row)

os.makedirs(out, exist_ok=True)
json.dump({"runs": [{"label": l, "evidence": p, "environment": e} for l, p, e, _ in runs], "tests": rows},
          open(os.path.join(out, "matrix-results.json"), "w"), indent=1)
fields = list(rows[0].keys())
with open(os.path.join(out, "matrix.csv"), "w", newline="") as f:
    w = csv.DictWriter(f, fieldnames=fields)
    w.writeheader()
    w.writerows(rows)
with open(os.path.join(out, "summary.md"), "w") as f:
    f.write("# Compatibility results\n\n")
    for l, p, e, _ in runs:
        f.write(f"- **{l}**: {e.get('engine', '?')}, {e.get('arch', '?')}, kernel {e.get('kernel', '?')}, "
                f"harness {e.get('harness', '?')}, started {e.get('started', '?')}\n")
    f.write("\n| ID | Priority | Image | " + " | ".join(l for l, *_ in runs) + " |\n")
    f.write("|---|---|---|" + "---|" * len(runs) + "\n")
    for r in rows:
        cells = [f"{r[l]} ({r[l + '_checks']})" if r[l + "_checks"] else r[l] for l, *_ in runs]
        f.write(f"| {r['id']} | {r['priority']} | `{r['image']}` | " + " | ".join(cells) + " |\n")
    f.write("\n## Failures\n\n")
    for r in rows:
        for l, *_ in runs:
            if r[l + "_first_failure"]:
                f.write(f"- {r['id']} / {l}: {r[l + '_first_failure']}\n")
print(os.path.join(out, "summary.md"))
