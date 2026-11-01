#!/bin/bash
# Snap path end to end on a Linux host, against the real Snap Store: the
# daemon with --guest-root /, a real PRoot, and thothdock-snap installing GNU
# hello (base core20) and running it. Needs root (for /snap), network,
# unsquashfs, and a user "thoth" to run apps as.
#
#   tests/regression/snap.sh THOTHDOCK_BINARY SNAP_BINARY PROOT_BINARY
set -u
TD="$(realpath "$1")"; SNAP="$(realpath "$2")"; PROOT="$(realpath "$3")"
WORK="$(mktemp -d)"; chmod 755 "$WORK"; chown thoth "$WORK"
pass=0 fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
no() { echo "FAIL $*"; fail=$((fail+1)); }
install -m 755 "$SNAP" /usr/local/bin/snap

# As root, so the daemon owns the guest's files as the Android app owns its
# guest (here the guest is this machine's root, owned by root).
"$TD" serve --root "$WORK/root" --proot "$PROOT" --guest-root / > "$WORK/daemon.log" 2>&1 &
trap 'pkill -f "thothdock serve" 2>/dev/null; rm -rf "$WORK"' EXIT
export THOTHDOCK_SOCKET="$WORK/root/run/thothdock.sock"
for _ in $(seq 50); do [ -S "$THOTHDOCK_SOCKET" ] && break; sleep 0.1; done
chmod 755 "$WORK/root" "$WORK/root/run"; chmod 666 "$THOTHDOCK_SOCKET" 2>/dev/null
[ -S "$THOTHDOCK_SOCKET" ] && ok "daemon up" || { no "daemon"; cat "$WORK/daemon.log"; exit 1; }

snap find hello | grep -q "^hello " && ok "snap find" || no "find"
snap info hello | grep -q "base: *core20" && ok "snap info shows base core20" || no "info"

out=$(snap install hello 2>&1); rc=$?
[ $rc = 0 ] && ok "snap install hello (and its base)" || no "install: $out"
snap list | grep -q "^core20 " && snap list | grep -q "^hello " && ok "snap list" || no "list: $(snap list)"
[ -x /snap/bin/hello ] && [ -x /snap/bin/hello.universe ] && ok "launchers in /snap/bin" || no "launchers"

out=$(su thoth -c "THOTHDOCK_SOCKET='$THOTHDOCK_SOCKET' /snap/bin/hello" 2>&1)
[ "$out" = "Hello, world!" ] && ok "hello runs: $out" || no "hello: $out"
out=$(su thoth -c "THOTHDOCK_SOCKET='$THOTHDOCK_SOCKET' /snap/bin/hello.universe" 2>&1)
[ "$out" = "Hello, universe!" ] && ok "app environment and command-chain: $out" || no "universe: $out"
out=$(su thoth -c "THOTHDOCK_SOCKET='$THOTHDOCK_SOCKET' snap run hello --version" 2>&1 | head -1)
grep -q "GNU Hello" <<<"$out" && ok "arguments pass through" || no "args: $out"
[ -d /home/thoth/snap/hello ] && ok "SNAP_USER_DATA created" || no "no ~/snap/hello"

# A service: run detached through the same engine, logged, stopped.
mkdir -p /snap/svc-test/1/meta /snap/svc-test/1/bin /var/snap/svc-test/common
printf 'name: svc-test\nversion: "1"\nbase: core20\napps:\n  tick:\n    command: bin/tick\n    daemon: simple\n' > /snap/svc-test/1/meta/snap.yaml
printf '#!/bin/sh\nwhile :; do echo tick; sleep 1; done\n' > /snap/svc-test/1/bin/tick; chmod 755 /snap/svc-test/1/bin/tick
ln -sfn 1 /snap/svc-test/current
python3 - <<'EOF'
import json; p='/var/lib/thothdock-snaps/state.json'; s=json.load(open(p))
s['snaps']['svc-test']={"revision":1,"version":"1","channel":"local","type":"app","base":"core20","confinement":"strict","apps":[],"daemons":["tick"]}
json.dump(s,open(p,'w'))
EOF
snap start svc-test.tick >/dev/null && ok "snap start" || no "start"
sleep 3
snap services | grep -q "svc-test.tick *active" && ok "service active (engine sandbox)" || no "services: $(snap services)"
snap logs svc-test.tick | grep -q tick && ok "service log" || no "logs"
snap stop svc-test.tick >/dev/null && sleep 1 && snap services | grep -q "svc-test.tick *inactive" && ok "snap stop" || no "stop: $(snap services)"

# Hooks: a snap's configure hook runs at install, as snapd's would.
mkdir -p "$WORK/hooksnap/meta/hooks" "$WORK/hooksnap/bin"
printf 'name: hook-test\nversion: "1"\nbase: core20\napps:\n  hook-test:\n    command: bin/true\n' > "$WORK/hooksnap/meta/snap.yaml"
printf '#!/bin/sh -e\nmkdir -p $SNAP_COMMON/etc && echo configured > $SNAP_COMMON/etc/marker\n' > "$WORK/hooksnap/meta/hooks/configure"
chmod 755 "$WORK/hooksnap/meta/hooks/configure"
python3 - "$WORK" <<'PY'
import json,sys; p='/var/lib/thothdock-snaps/state.json'; s=json.load(open(p))
s['snaps']['hook-test']={"revision":1,"version":"1","channel":"local","type":"app","base":"core20","confinement":"strict","apps":["hook-test"],"daemons":[]}
json.dump(s,open(p,'w'))
PY
mkdir -p /snap/hook-test /var/snap/hook-test/common && cp -a "$WORK/hooksnap" /snap/hook-test/1 && ln -sfn 1 /snap/hook-test/current
snap run --hook=configure hook-test >/dev/null 2>&1
[ "$(cat /var/snap/hook-test/common/etc/marker 2>/dev/null)" = configured ] && ok "configure hook runs in the snap's sandbox" || no "hook did not run"
snap remove hook-test >/dev/null

# A base cannot be removed while an app needs it.
snap remove core20 2>&1 | grep -q "uses it as its base" && ok "base in use cannot be removed" || no "base removal"
snap remove svc-test >/dev/null; snap remove hello >/dev/null && snap remove core20 >/dev/null
[ ! -e /snap/hello ] && [ ! -e /snap/bin/hello ] && ok "remove cleans /snap and launchers" || no "remove left files"

echo "RESULT pass=$pass fail=$fail"
[ "$fail" = 0 ]
