#!/bin/bash
# The bundled Docker CLI, run as the app uid against the daemon socket (debug build only: needs run-as).
A="${ADB:-adb}"; P="${PKG:-com.thothterm.debian.rc.thothdock}"
NLD=$($A shell dumpsys package "$P" | grep -m1 legacyNativeLibraryDir | sed 's/^[^=]*=//' | tr -d '\r')/arm64
$A shell "run-as $P $NLD/libdocker.so -H unix:///data/data/$P/files/thothdock/sock/thothdock.sock $*"
