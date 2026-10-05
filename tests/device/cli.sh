#!/bin/bash
# The bundled Docker CLI, run as the app uid against the daemon socket.
adb shell "run-as com.thothterm.debian.rc.thothdock /data/app/~~LgUw2dngW4M6N6wfEYf3tw==/com.thothterm.debian.rc.thothdock-jO_OfJ8IwYHl007DTTBJ-g==/lib/arm64/libdocker.so -H unix:///data/data/com.thothterm.debian.rc.thothdock/files/thothdock/sock/thothdock.sock $*"
