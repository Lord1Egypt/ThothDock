# Sourced under run-as com.thothterm.debian.qa.sysvipc (QA package only).
L=$(dirname "$(pm path com.thothterm.debian.qa.sysvipc 2>/dev/null | sed 's/package://')")/lib/arm64
F=/data/user/0/com.thothterm.debian.qa.sysvipc/files
Q=$F/tdqa
dk() { HOME=$Q DOCKER_CONFIG=$Q/cli "$L/libdocker.so" -H "unix://$Q/sock" "$@"; }
