#!/bin/bash
# Idle cost of the engine and everything it runs, read over adb (read only).
#
#   tests/device/perf-sample.sh PACKAGE SECONDS LABEL
#
# Two samples SECONDS apart of the engine process (RSS, threads) and of every
# process of the app's uid: CPU ticks (utime+stime) and context switches. The
# difference divided by the time is the idle cost; wakeups are approximated by
# voluntary context switches. Set ADB="adb -s SERIAL". Prints one CSV line.
set -u
PKG=$1 SEC=$2 LABEL=$3
ADB=${ADB:-adb}
snap() {
    # One ps call finds the uid's processes; only those are read.
    $ADB shell "
u=\$(ps -o USER -p \$(pidof $PKG | cut -d' ' -f1) | tail -1)
for p in \$(ps -A -o USER,PID | awk -v u=\$u '\$1==u{print \$2}'); do
  s=\$(cat /proc/\$p/stat 2>/dev/null) || continue
  s=\${s##*) }
  set -- \$s
  tag=\$(tr '\\0' ' ' < /proc/\$p/cmdline 2>/dev/null | awk '{print (\$1 ~ /libthothdock/) ? \$2 : \"-\"}')
  awk -v p=\$p -v ut=\${12} -v st=\${13} -v tag=\"\$tag\" '/^voluntary_ctxt/{v=\$2} /^nonvoluntary_ctxt/{n=\$2} /^VmRSS/{r=\$2} /^Threads/{t=\$2} END{print p, ut+st, v, n, r, t, tag}' /proc/\$p/status
done" 2>/dev/null | tr -d '\r'
}
A=$(snap); T0=$(date +%s); sleep "$SEC"; B=$(snap); T1=$(date +%s)
python3 -I - "$LABEL" "$((T1-T0))" "$A" "$B" <<'PY'
import sys
label, dt, a, b = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
def parse(t):
    d = {}
    for l in t.splitlines():
        f = l.split()
        if len(f) == 7: d[int(f[0])] = list(map(int, f[1:6])) + [f[6]]
    return d
A, B = parse(a), parse(b)
common = A.keys() & B.keys()
ticks = sum(B[p][0] - A[p][0] for p in common)
vol = sum(B[p][1] - A[p][1] for p in common)
procs = len(B)
rss = sum(v[3] for v in B.values())
thr = sum(v[4] for v in B.values())
def part(tag):
    ps = [p for p in common if B[p][5] == tag]
    t = sum(B[p][0] - A[p][0] for p in ps); v = sum(B[p][1] - A[p][1] for p in ps)
    return len(ps), sum(B[p][3] for p in ps), sum(B[p][4] for p in ps), t / 100 / dt * 100, v / dt
print("label,seconds,procs,total_rss_kb,total_threads,all_cpu_pct,all_vol_ctxt_s,engine_rss_kb,engine_threads,engine_cpu_pct,engine_vol_ctxt_s,panel_procs,panel_cpu_pct")
e = part("serve"); pn = part("panel")
print(f"{label},{dt},{procs},{rss},{thr},{ticks/100/dt*100:.2f},{vol/dt:.1f},{e[1]},{e[2]},{e[3]:.3f},{e[4]:.1f},{pn[0]},{pn[3]:.3f}")
PY
