#!/bin/bash
# Flatpak sandbox path, end to end on a Linux host: the real daemon with
# --guest-root, a real PRoot, and the bwrap shim given a flatpak-shaped
# command line. No flatpak or Flathub needed.
#
#   tests/regression/sandbox.sh THOTHDOCK_BINARY BWRAP_SHIM PROOT_BINARY BUSYBOX_STATIC
set -u
TD="$(realpath "$1")"; SHIM="$(realpath "$2")"; PROOT="$(realpath "$3")"; BB="$(realpath "$4")"
WORK="$(mktemp -d)"; GUEST="$WORK/guest"
pass=0 fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
no() { echo "FAIL $*"; fail=$((fail+1)); }

# A guest with a "runtime" (busybox as /usr) and an "app" under the user's flatpak dir.
RT="$GUEST/home/thoth/.local/share/flatpak/runtime/rt/files"
APP="$GUEST/home/thoth/.local/share/flatpak/app/org.example.Tool/files"
mkdir -p "$RT/bin" "$APP/bin" "$GUEST/home/thoth/.var/app/org.example.Tool"
cp "$BB" "$RT/bin/busybox"
for a in sh cat echo id ls; do ln -s busybox "$RT/bin/$a"; done
printf '#!/usr/bin/sh\necho "tool: $FLATPAK_ID $*"; cat /etc/passwd | head -1; id -u; cat\n' > "$APP/bin/tool"
chmod 755 "$APP/bin/tool"
ln -s .local/share/flatpak/app "$GUEST/home/thoth/apps"

"$TD" serve --root "$WORK/root" --proot "$PROOT" --guest-root "$GUEST" > "$WORK/daemon.log" 2>&1 &
DPID=$!
trap 'kill $DPID 2>/dev/null; wait $DPID 2>/dev/null; rm -rf "$WORK"' EXIT
export THOTHDOCK_SOCKET="$WORK/root/run/thothdock.sock"
for _ in $(seq 50); do [ -S "$THOTHDOCK_SOCKET" ] && break; sleep 0.1; done
[ -S "$THOTHDOCK_SOCKET" ] && ok "daemon up with --guest-root" || { no "daemon did not start"; cat "$WORK/daemon.log"; exit 1; }

common=(--ro-bind /home/thoth/.local/share/flatpak/runtime/rt/files /usr
        --ro-bind /home/thoth/apps/org.example.Tool/files /app
        --symlink usr/bin /bin --proc /proc --dev /dev --dir /tmp
        --setenv PATH /app/bin:/usr/bin --setenv FLATPAK_ID org.example.Tool
        --unshare-pid --die-with-parent --new-session)

# 1: output, stdin, --file contents, uid, exit code, --info-fd.
out=$(echo stdin-data | "$SHIM" "${common[@]}" --perms 0644 --file 9 /etc/passwd --info-fd 8 -- tool arg1 \
      9< <(printf 'thoth:x:1000:1000::/home/thoth:/bin/sh\n') 8> "$WORK/info.json"); rc=$?
[ "$rc" = 0 ] && ok "tool exits 0" || no "tool exit $rc"
grep -q "^tool: org.example.Tool arg1$" <<<"$out" && ok "argv, PATH lookup, env" || no "output: $out"
grep -q "^thoth:x:1000:1000" <<<"$out" && ok "--file content visible" || no "no /etc/passwd from --file"
grep -q "^stdin-data$" <<<"$out" && ok "stdin reaches the sandbox" || no "stdin missing"
grep -q '"child-pid": [1-9]' "$WORK/info.json" && ok "--info-fd gets child-pid" || no "info-fd: $(cat "$WORK/info.json")"

# 2: exit code passes through.
"$SHIM" "${common[@]}" -- sh -c 'exit 42'; rc=$?
[ "$rc" = 42 ] && ok "exit code 42 passes through" || no "exit code $rc"

# 3: args from an --args descriptor, as flatpak sends them.
out=$("$SHIM" --args 7 -- echo via-args 7< <(printf -- '--ro-bind\0/home/thoth/.local/share/flatpak/runtime/rt/files\0/usr\0--symlink\0usr/bin\0/bin\0'))
[ "$out" = via-args ] && ok "--args descriptor" || no "--args output: $out"

# 4: a guest symlink climbing out with .. stays inside the guest.
ln -s ../../../../../../../.. "$GUEST/home/thoth/up"
out=$("$SHIM" "${common[@]}" --ro-bind /home/thoth/up /up -- ls /up/home)
grep -q "^thoth$" <<<"$out" && ok "escaping symlink resolves inside the guest" || no "escape: $out"

# 5: killing the shim kills a --die-with-parent sandbox.
"$SHIM" "${common[@]}" -- sh -c 'sleep 300' & SPID=$!
sleep 2; kill -TERM $SPID; wait $SPID 2>/dev/null
for _ in $(seq 50); do pgrep -f "sleep 300" >/dev/null || break; sleep 0.2; done
pgrep -f "sleep 300" >/dev/null && no "sandbox outlived the shim" || ok "sandbox ended with the shim"

# 6: nothing left behind.
sleep 1
[ -z "$(ls -A "$WORK/root/sandboxes" 2>/dev/null)" ] && ok "sandbox roots removed" || no "left: $(ls "$WORK/root/sandboxes")"

# 7: unknown options are refused, not half-applied.
"$SHIM" --frobnicate -- true 2>/dev/null && no "unknown option accepted" || ok "unknown option refused"

echo "RESULT pass=$pass fail=$fail"
[ "$fail" = 0 ]
