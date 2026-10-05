#!/bin/bash
A="${ADB:-adb}"; Q=com.thothterm.debian.rc.thothdock
echo "--- $1"
$A shell "ps -A -o PID,USER,ARGS | grep -E 'libthothdock|libdocker|libproot' | grep -v grep | cut -c1-110"
echo "daemons=$($A shell pidof libthothdock.so | wc -w) sock=$($A shell "run-as $Q ls files/thothdock/sock 2>&1" | tr '\n' ' ')"
echo "tcp-listeners-owned-by-app: $($A shell "grep -c ' 0A ' /proc/net/tcp /proc/net/tcp6 2>/dev/null | tr '\n' ' '")"
$A shell "run-as $Q ls files/thothdock/tmp 2>&1 | wc -l" | sed 's/^/tmp-entries: /'
$A shell "run-as $Q sh -c 'ls -d files/thothdock/containers/* 2>/dev/null | wc -l'" | sed 's/^/containers-on-disk: /'
