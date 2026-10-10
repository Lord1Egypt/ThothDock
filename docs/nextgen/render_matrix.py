#!/usr/bin/env python3
"""Render docs/nextgen/compatibility.json as COMPATIBILITY_MATRIX.md.

    python3 docs/nextgen/render_matrix.py            write the file
    python3 docs/nextgen/render_matrix.py --check    fail if it is stale
"""
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))


def cell(s):
    return str(s).replace("|", "\\|").replace("\n", " ")


def render(data):
    out = ["# Docker compatibility matrix", "",
           "Generated from `compatibility.json` by `render_matrix.py`; edit the JSON. "
           f"Updated {data['updated']}, engine {data['engine']}.", "",
           "A status comes only from a test, never from reading code. API acceptance "
           "is not behavioural equivalence, and neither is a security boundary "
           "(`SECURITY_MODEL.md`).", "", "| Status | Meaning |", "|---|---|"]
    out += [f"| `{k}` | {cell(v)} |" for k, v in data["statuses"].items()]
    out += ["", "| Key | Verified by |", "|---|---|"]
    out += [f"| {k} | {cell(v)} |" for k, v in data["verification_keys"].items()]
    counts = {}
    for f in data["features"]:
        counts[f["status"]] = counts.get(f["status"], 0) + 1
    out += ["", "Totals: " + ", ".join(f"{k} {v}" for k, v in sorted(counts.items())), "",
            "| Feature | Status | Verified | Tests | Android | Security | Overhead | Known failure modes |",
            "|---|---|---|---|---|---|---|---|"]
    for f in data["features"]:
        out.append("| " + " | ".join(cell(x) for x in [
            f["feature"], "`" + f["status"] + "`", " ".join(f["verified"]) or "—", f["tests"],
            f["android"] or "—", f["security"] or "—", f["overhead"] or "—", f["failure_modes"] or "—"]) + " |")
    return "\n".join(out) + "\n"


def main():
    with open(os.path.join(HERE, "compatibility.json")) as fh:
        data = json.load(fh)
    allowed = set(data["statuses"])
    for f in data["features"]:
        if f["status"] not in allowed:
            sys.exit(f"{f['id']}: unknown status {f['status']}")
        if f["status"] != "NOT_TESTED" and not f["verified"]:
            sys.exit(f"{f['id']}: a status other than NOT_TESTED needs a verification key")
    text = render(data)
    target = os.path.join(HERE, "COMPATIBILITY_MATRIX.md")
    if "--check" in sys.argv:
        with open(target) as fh:
            if fh.read() != text:
                sys.exit("COMPATIBILITY_MATRIX.md is stale: run docs/nextgen/render_matrix.py")
        return
    with open(target, "w") as fh:
        fh.write(text)


if __name__ == "__main__":
    main()
