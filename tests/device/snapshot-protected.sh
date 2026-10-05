#!/bin/bash
# SHA-256, path and mtime of the apps the RC work must not touch.
for p in com.thothterm com.thothterm.devel com.thothterm.ubuntu com.thothterm.debian com.thothterm.arch com.thothterm.security com.lord1egypt.pocketclaw; do
  path=$(adb shell pm path $p 2>/dev/null | tr -d '\r' | sed -n 's/^package://p' | head -1)
  if [ -z "$path" ]; then echo "$p ABSENT"; continue; fi
  h=$(adb shell sha256sum "$path" | tr -d '\r' | cut -d' ' -f1)
  m=$(adb shell stat -c '%y' "$path" | tr -d '\r')
  echo "$p $h $path $m"
done
