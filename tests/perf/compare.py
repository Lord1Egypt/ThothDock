#!/usr/bin/env python3
"""Compare two sets of tests/perf/baseline.sh results (ticket P0-04).

    tests/perf/compare.py 'BASE_GLOB' 'CANDIDATE_GLOB'

Prints the median of every metric over all runs and windows of each set, and
the relative change. Medians of repeated, interleaved runs on a quiet machine
are the only comparison docs/nextgen/PERFORMANCE_BUDGET.md accepts; the
fsync-bound fs_write figure varies several-fold on virtual disks.
"""
import glob
import re
import statistics
import sys


def load(pattern):
    runs = []
    for path in sorted(glob.glob(pattern)):
        values = {}
        with open(path) as f:
            for line in f:
                if "=" in line and not line.startswith("#"):
                    key, value = line.strip().split("=", 1)
                    try:
                        values[key] = float(value)
                    except ValueError:
                        pass
        runs.append(values)
    if not runs:
        sys.exit(f"no results match {pattern}")
    return runs


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    base, cand = load(sys.argv[1]), load(sys.argv[2])
    groups = {}
    for key in base[0]:
        if key in cand[0]:
            groups.setdefault(re.sub(r"\.(window|run)\d+", "", key), []).append(key)
    print(f"{'metric':52} {'base median':>12} {'candidate':>10} {'change':>8}")
    for group, keys in groups.items():
        if group in ("cpus", "clock_overhead_ms", "mem_total_kib"):
            continue
        b = statistics.median([r[k] for r in base for k in keys if k in r])
        c = statistics.median([r[k] for r in cand for k in keys if k in r])
        change = f"{(c - b) / b * 100:+.0f}%" if b else ("0" if c == 0 else "new")
        print(f"{group:52} {b:12.0f} {c:10.0f} {change:>8}")


if __name__ == "__main__":
    main()
