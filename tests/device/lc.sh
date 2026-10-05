#!/bin/bash
# Lifecycle state of the RC app: processes, socket, pid file, listener on 18090, containers.
echo "--- $1 ($(date +%T))"
echo "daemon pids: $(adb shell pidof libthothdock.so | tr -d '\r')"
echo "proot workers of RC app: $(adb shell "ps -A -o PID,ARGS | grep libproot | grep '/com.thothterm.debian.rc.thothdock/' | grep -v grep" | wc -l)"
echo "app procs: $(adb shell "ps -A -o NAME | grep '^com.thothterm.debian.rc.thothdock'" | tr '\r\n' '  ')"
echo "socket: $(adb shell "run-as com.thothterm.debian.rc.thothdock ls files/thothdock/sock 2>&1" | tr -d '\r' | tr '\n' ' ')"
echo "pidfile: $(adb shell "run-as com.thothterm.debian.rc.thothdock sh -c 'cat files/thothdock/*.pid files/thothdock/run/*.pid 2>/dev/null'" | tr -d '\r' | tr '\n' ' ')"
echo "listener 18090 (0x46AA): $(adb shell 'grep -c ":46AA 00000000:0000 0A" /proc/net/tcp /proc/net/tcp6' | tr -d '\r' | tr '\n' ' ')"
echo "containers: $(./cli.sh ps -a --format '{{.Names}}={{.Status}}' 2>&1 | tr '\r\n' ';')"
