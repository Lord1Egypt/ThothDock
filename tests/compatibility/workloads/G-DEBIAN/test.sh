# G-DEBIAN: minimal Linux semantics on Debian trixie slim (glibc, dash).
# timeout: 300
. ../G-ALPINE/minimal.sh
minimal_env debian:trixie-slim
x=$(dk run --rm --network "$N" debian:trixie-slim sh -c "ls /usr/lib/*/libc.so.6 >/dev/null && getconf GNU_LIBC_VERSION")
ok "$(echo "$x" | grep -q glibc; echo $?)" "glibc reports its version ($x)"
no_leftovers
