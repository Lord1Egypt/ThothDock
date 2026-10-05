#!/bin/bash
# Guarded UI driver by element text. Refuses unless the RC app is in front.
A=adb; PKG=com.thothterm.debian.rc.thothdock
guard() { $A shell dumpsys activity activities | grep -m1 topResumedActivity | grep -q "$PKG" || { echo "GUARD: $PKG not in front; refusing input" >&2; exit 9; }; }
dump() { $A shell uiautomator dump /sdcard/ui.xml >/dev/null 2>&1; $A shell cat /sdcard/ui.xml; }
case "$1" in
  tap) guard; dump | python3 -c '
import sys,re
x=sys.stdin.read(); want=sys.argv[1]; idx=int(sys.argv[2]) if len(sys.argv)>2 else 0
hits=[]
for m in re.finditer(r"<node [^>]*>",x):
    n=m.group(0)
    t=re.search(r"text=\"([^\"]*)\"",n); d=re.search(r"content-desc=\"([^\"]*)\"",n); b=re.search(r"bounds=\"\[(\d+),(\d+)\]\[(\d+),(\d+)\]\"",n)
    if b and ((t and t.group(1)==want) or (d and d.group(1)==want)):
        a=list(map(int,b.groups())); hits.append(((a[0]+a[2])//2,(a[1]+a[3])//2))
if len(hits)<=idx: print("NOTFOUND",want,file=sys.stderr); sys.exit(3)
print(*hits[idx])' "$2" "${3:-0}" > /tmp/uia.xy || exit $?
     $A shell input tap $(cat /tmp/uia.xy) ;;
  texts) guard; dump | grep -o 'text="[^"]*"' | grep -v 'text=""' ;;
  shot) $A exec-out screencap -p > "$2" ;;
esac
