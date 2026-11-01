# G-BUSYBOX: minimal Linux semantics on BusyBox (static, no package manager).
# timeout: 240
. ../G-ALPINE/minimal.sh
minimal_env busybox:1.37
no_leftovers
