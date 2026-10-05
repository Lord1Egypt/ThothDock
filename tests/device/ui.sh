#!/bin/bash
# Guarded driver: input only reaches the RC app, never anything else.
A="${ADB:-adb}"; PKG=com.thothterm.debian.rc.thothdock
guard() { $A shell dumpsys activity activities | grep -m1 topResumedActivity | grep -q "$PKG" || { echo "GUARD: $PKG not in front; refusing input" >&2; exit 9; }; }
case "$1" in
  cmd) guard; t="${2// /%s}"; $A shell "input text '$t'"; $A shell input keyevent KEYCODE_ENTER ;;
  key) guard; $A shell input keyevent "$2" ;;
  shot) $A exec-out screencap -p > "$2" ;;
  front) $A shell dumpsys activity activities | grep -m1 topResumedActivity ;;
esac
