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
snap start svc-test.tick | grep -q "already running" && ok "a running service is not started twice" || no "second start"
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

# snapctl: configuration, and a service restarted from a hook. The hook reads
# a default, sets one, and asks for a restart; "snap set" runs it again.
mkdir -p "$WORK/ctlsnap/meta/hooks" "$WORK/ctlsnap/bin"
printf 'name: ctl-test\nversion: "1"\nbase: core20\napps:\n  tick:\n    command: bin/tick\n    daemon: simple\n' > "$WORK/ctlsnap/meta/snap.yaml"
printf '#!/bin/sh\nwhile :; do echo tick $(snapctl get greeting); sleep 1; done\n' > "$WORK/ctlsnap/bin/tick"
printf '#!/bin/sh -e\n[ -n "$(snapctl get greeting)" ] || snapctl set greeting=hello ports.http=80 private.x=1\ncase "$(snapctl get greeting)" in bad) echo "bad greeting" >&2; exit 1;; esac\nsnapctl restart "$SNAP_INSTANCE_NAME.tick"\n' > "$WORK/ctlsnap/meta/hooks/configure"
chmod 755 "$WORK/ctlsnap/bin/tick" "$WORK/ctlsnap/meta/hooks/configure"
python3 - <<'PY'
import json; p='/var/lib/thothdock-snaps/state.json'; s=json.load(open(p))
s['snaps']['ctl-test']={"revision":1,"version":"1","channel":"local","type":"app","base":"core20","confinement":"strict","apps":[],"daemons":["tick"]}
json.dump(s,open(p,'w'))
PY
mkdir -p /snap/ctl-test /var/snap/ctl-test/common && cp -a "$WORK/ctlsnap" /snap/ctl-test/1 && ln -sfn 1 /snap/ctl-test/current
snap run --hook=configure ctl-test >/dev/null 2>&1
[ "$(snap get ctl-test greeting 2>&1)" = hello ] && ok "snapctl set in a hook, snap get reads it" || no "snapctl set: $(snap get ctl-test 2>&1)"
[ "$(snap get ctl-test ports.http 2>&1)" = 80 ] && ok "a number keeps its type" || no "ports.http: $(snap get ctl-test ports.http 2>&1)"
snap get ctl-test 2>&1 | grep -q private && no "private keys are shown" || ok "private keys are hidden from snap get"
sleep 2
snap services | grep -q "ctl-test.tick *active" && ok "snapctl restart from a hook starts the service" || no "restart: $(snap services)"
snap logs ctl-test.tick | grep -q "tick hello" && ok "the service reads the configuration through snapctl" || no "service log: $(snap logs ctl-test.tick 2>&1 | tail -2)"
snap set ctl-test greeting=bad >/dev/null 2>&1 && no "a failing configure hook was accepted" || ok "snap set with a failing hook is refused"
[ "$(snap get ctl-test greeting 2>&1)" = hello ] && ok "...and the old configuration stays" || no "config after refusal: $(snap get ctl-test greeting 2>&1)"
snap set ctl-test greeting=hi >/dev/null 2>&1; sleep 3
snap logs ctl-test.tick | tail -3 | grep -q "tick hi" && ok "snap set restarts the service with the new value" || no "set/restart: $(snap logs ctl-test.tick | tail -2)"
snap stop ctl-test.tick >/dev/null; snap remove ctl-test >/dev/null

# A base cannot be removed while an app needs it.
snap remove core20 2>&1 | grep -q "uses it as its base" && ok "base in use cannot be removed" || no "base removal"
snap remove svc-test >/dev/null; snap remove hello >/dev/null && snap remove core20 >/dev/null
[ ! -e /snap/hello ] && [ ! -e /snap/bin/hello ] && ok "remove cleans /snap and launchers" || no "remove left files"

echo "RESULT pass=$pass fail=$fail"
[ "$fail" = 0 ]
