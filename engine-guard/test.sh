#!/bin/sh
# Engine Guard tests.
#
#   engine-guard/test.sh            run both parts
#   CAPTURE=DIR engine-guard/test.sh  also keep the apt records part 2 saw
#
# Part 1 feeds the hook apt protocol-3 records recorded from real apt runs
# (engine-guard/testdata). Part 2 builds the guard packages and drives real
# apt-get and dpkg against a throwaway root in a user namespace
# (unshare -rm; no real root needed), so the hook sees what apt really sends.
# Part 2 is skipped, loudly, where unshare -r or apt-get is unavailable.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
fails=0
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
PROTECTED="docker.io docker-ce docker-engine moby-engine containerd containerd.io runc"

sed "s/@PROTECTED@/$PROTECTED/" "$HERE/engine-guard-hook.sh" > "$W/hook"
chmod +x "$W/hook"

# ---- part 1: recorded apt records ---------------------------------------
# expect NAME FILE allow|block
expect() {
    if "$W/hook" 3< "$2" 2> "$W/err"; then got=allow; else got=block; fi
    if [ "$got" = "$3" ]; then pass "$1 ($3)"; else fail "$1: wanted $3, got $got"; cat "$W/err"; fi
}
for f in "$HERE"/testdata/*.apt3; do
    n="$(basename "$f" .apt3)"
    case "$n" in
        block-*) expect "recorded $n" "$f" block ;;
        allow-*) expect "recorded $n" "$f" allow ;;
    esac
done
for p in $PROTECTED; do
    sed "s/^docker.io /$p /" "$HERE/testdata/block-stock-install.apt3" > "$W/rec"
    expect "stock $p replaces the placeholder" "$W/rec" block
done
# The message names what was refused.
"$W/hook" 3< "$HERE/testdata/block-remove-placeholder.apt3" 2>&1 | grep -q 'Refusing to remove the ThothDock Docker Engine placeholder: runc' \
    && pass "removal message names the placeholder" || fail "removal message names the placeholder"
"$W/hook" 3< "$HERE/testdata/block-stock-install.apt3" 2>&1 | grep -q 'docker.io(26.1.5-1)' \
    && pass "message names the package and version" || fail "message names the package and version"

if ! command -v apt-get >/dev/null || ! unshare -rm true 2>/dev/null; then
    echo "SKIP part 2: needs apt-get and unprivileged user namespaces (unshare -rm)"
    [ "$fails" -eq 0 ] || { echo "$fails FAILED"; exit 1; }
    exit 0
fi

# ---- part 2: real apt and dpkg ------------------------------------------
G="$W/guard"
sh "$HERE/build.sh" "$W/guard-debs" > /dev/null
dpkg-deb -x "$W/guard-debs/thothdock-engine-guard_1.0+thothdock.3_all.deb" "$G"
R="$W/root"
mkdir -p "$R/var/lib/dpkg" "$R/inst" "$R/cache/archives/partial" "$R/state/lists/partial" "$W/repo" "$W/pool"
: > "$R/var/lib/dpkg/status"
sed "s#/usr/lib/thothdock#$G/usr/lib/thothdock#" "$G/etc/apt/apt.conf.d/99thothdock-engine-guard" > "$W/99guard.conf"
if [ -n "${CAPTURE:-}" ]; then
    mkdir -p "$CAPTURE"
    # Keep what apt sent, then give it to the real hook.
    cat > "$W/tee-hook" <<TEE
#!/bin/sh
n=\$(ls "$CAPTURE" | wc -l)
cat <&3 > "$CAPTURE/\$\$.\$n.raw"
exec "$G/usr/lib/thothdock/engine-guard-hook" 3< "$CAPTURE/\$\$.\$n.raw"
TEE
    chmod +x "$W/tee-hook"
    sed -i "s#$G/usr/lib/thothdock/engine-guard-hook#$W/tee-hook#g" "$W/99guard.conf"
fi
mkdir -p "$W/aptconf.d" "$W/prefs.d"
cp "$W/99guard.conf" "$W/aptconf.d/"
cp "$G/etc/apt/preferences.d/thothdock-engine-guard" "$W/prefs.d/"
echo "deb [trusted=yes] file:$W/repo ./" > "$W/sources.list"
cat > "$W/apt.conf" <<EOF2
Dir::State::status "$R/var/lib/dpkg/status";
Dir::State "$R/state";
Dir::Cache "$R/cache";
Dir::Etc::sourcelist "$W/sources.list";
Dir::Etc::sourceparts "/dev/null";
Dir::Etc::parts "$W/aptconf.d";
Dir::Etc::preferences "/dev/null";
Dir::Etc::preferencesparts "$W/prefs.d";
Dir::Log "$W/log";
APT::Sandbox::User "";
Debug::NoLocking "1";
DPkg::Options:: "--admindir=$R/var/lib/dpkg";
DPkg::Options:: "--instdir=$R/inst";
DPkg::Options:: "--force-not-root";
DPkg::Options:: "--force-script-chrootless";
DPkg::Options:: "--no-triggers";
DPkg::Options:: "--log=/dev/null";
EOF2
mkdir -p "$W/log"

mkdeb() { # name version
    d="$W/b-$1-$2"; mkdir -p "$d/DEBIAN"
    printf 'Package: %s\nVersion: %s\nArchitecture: all\nMaintainer: t <t@example.invalid>\nDescription: test package\n' "$1" "$2" > "$d/DEBIAN/control"
    dpkg-deb --root-owner-group -Znone --build "$d" "$W/pool/$1_${2#*:}_all.deb" > /dev/null
}
for p in $PROTECTED; do
    mkdeb "$p" 9999:1.0+thothdock.1
    mkdeb "$p" 26.1.5-1
done
mkdeb docker.io 9999:1.0+thothdock.2
mkdeb foo 1.0
mkdeb foo 2.0
mkdeb bar 1.0

repo() { # rebuild the repository from the named pool files
    rm -f "$W/repo"/*.deb "$W/repo/Packages"
    for f in "$@"; do cp "$W/pool/$f" "$W/repo/"; done
    ( cd "$W/repo" && apt-ftparchive packages . > Packages )
}
apt() { # real apt-get as "root" in a user namespace
    unshare -rm env APT_CONFIG="$W/apt.conf" apt-get -y -q "$@" > "$W/out" 2>&1
}
installed() { dpkg-query --admindir="$R/var/lib/dpkg" -W -f='${Version}' "$1" 2>/dev/null || true; }
# ok NAME CMD...: command must succeed; no NAME CMD...: apt must refuse
ok() { n="$1"; shift; if apt "$@"; then pass "$n"; else fail "$n"; cat "$W/out"; fi; }
no() { n="$1"; shift; if apt "$@"; then fail "$n (apt succeeded)"; else if grep -q 'Refusing to install the stock Docker Engine' "$W/out"; then pass "$n"; else fail "$n (refused for another reason)"; cat "$W/out"; fi; fi; }
is() { [ "$(installed "$2")" = "$3" ] && pass "$1" || { fail "$1: $2 is '$(installed "$2")', wanted '$3'"; }; }

all="$(cd "$W/pool" && ls | grep -v thothdock.2)"
repo $all
ok "apt update" update

# A fresh system: the stock engine must not install even by explicit version.
for p in $PROTECTED; do
    no "apt install stock $p on a clean system" install "$p=26.1.5-1"
    is "nothing of stock $p was installed" "$p" ""
done
ok "apt install the placeholder" install docker.io
is "the pin chose the placeholder, not stock" docker.io 9999:1.0+thothdock.1
no "apt install stock docker.io over the placeholder" install --allow-downgrades docker.io=26.1.5-1
is "refusal left the placeholder installed" docker.io 9999:1.0+thothdock.1

ok "apt install unrelated packages" install foo=1.0 bar
repo $(cd "$W/pool" && ls)
ok "apt update (repo with newer placeholder)" update
ok "apt upgrade" upgrade
is "apt upgrade took the newer ThothDock placeholder" docker.io 9999:1.0+thothdock.2
is "apt upgrade upgraded the unrelated package" foo 2.0
ok "apt full-upgrade" full-upgrade
is "apt full-upgrade left the placeholder alone" docker.io 9999:1.0+thothdock.2
ok "apt remove an unrelated package" remove bar
ok "apt reinstall the placeholder" install --reinstall docker.io
no "apt install the stock package over the upgraded placeholder" install --allow-downgrades docker.io=26.1.5-1

# ---- part 3: the registered guard package, as dpkg and apt see it -------
# The guard Depends on every placeholder and Breaks every lower version of
# those names, so dpkg refuses what apt's hook never sees (a direct dpkg call).
R2="$W/root2"
mkdir -p "$R2/var/lib/dpkg" "$R2/inst" "$R2/cache/archives/partial" "$R2/state/lists/partial"
: > "$R2/var/lib/dpkg/status"
sed "s#$R/#$R2/#g" "$W/apt.conf" > "$W/apt2.conf"
dp() { dpkg --admindir="$R2/var/lib/dpkg" --instdir="$R2/inst" --force-not-root --force-script-chrootless --no-triggers --log=/dev/null "$@" > "$W/out" 2>&1; }
apt2() { unshare -rm env APT_CONFIG="$W/apt2.conf" apt-get -y -q "$@" > "$W/out" 2>&1; }
installed2() { dpkg-query --admindir="$R2/var/lib/dpkg" -W -f='${Version}' "$1" 2>/dev/null || true; }
is2() { [ "$(installed2 "$2")" = "$3" ] && pass "$1" || fail "$1: $2 is '$(installed2 "$2")', wanted '$3'"; }
# refuse NAME PATTERN CMD...: the command must fail and its output match PATTERN
refuse() {
    n="$1"; pat="$2"; shift 2
    if "$@"; then fail "$n (succeeded)"; cat "$W/out"; elif grep -qi "$pat" "$W/out"; then pass "$n"; else fail "$n (refused for another reason)"; cat "$W/out"; fi
}
if dp -i "$W"/guard-debs/*.deb; then pass "placeholders and guard register with dpkg"; else fail "placeholders and guard register with dpkg"; cat "$W/out"; fi
is2 "guard registered" thothdock-engine-guard 1.0+thothdock.3
for p in $PROTECTED; do
    refuse "dpkg -i stock $p over its placeholder" 'conflicts with' dp -i "$W/pool/${p}_26.1.5-1_all.deb"
    refuse "dpkg -r placeholder $p" 'depends on' dp -r "$p"
    refuse "dpkg -P placeholder $p" 'depends on' dp -P "$p"
    is2 "placeholder $p still installed" "$p" 9999:1.0+thothdock.1
done
if dp -i "$W/pool/foo_1.0_all.deb"; then pass "dpkg -i an unrelated package"; else fail "dpkg -i an unrelated package"; cat "$W/out"; fi
if dp -r foo; then pass "dpkg -r an unrelated package"; else fail "dpkg -r an unrelated package"; cat "$W/out"; fi
# apt: the hook refuses removals, with its own message
repo $(cd "$W/pool" && ls | grep -v 'thothdock.2')
if apt2 update; then pass "apt update (guard registered)"; else fail "apt update (guard registered)"; cat "$W/out"; fi
for p in runc docker.io containerd.io thothdock-engine-guard; do
    refuse "apt remove $p" 'Refusing to remove the ThothDock Docker Engine placeholder' apt2 remove "$p"
    refuse "apt purge $p" 'Refusing to remove the ThothDock Docker Engine placeholder' apt2 purge "$p"
done
refuse "apt install a stock engine by version" 'Refusing to install the stock Docker Engine\|breaks\|Refusing to remove' apt2 install --allow-downgrades docker.io=26.1.5-1
is2 "placeholder docker.io survived" docker.io 9999:1.0+thothdock.1
# ordinary work is untouched
if apt2 install foo=1.0 bar; then pass "apt install unrelated packages (guard registered)"; else fail "apt install unrelated packages (guard registered)"; cat "$W/out"; fi
if apt2 upgrade; then pass "apt upgrade (guard registered)"; else fail "apt upgrade (guard registered)"; cat "$W/out"; fi
if apt2 full-upgrade; then pass "apt full-upgrade (guard registered)"; else fail "apt full-upgrade (guard registered)"; cat "$W/out"; fi
if apt2 remove bar; then pass "apt remove an unrelated package (guard registered)"; else fail "apt remove an unrelated package (guard registered)"; cat "$W/out"; fi
is2 "apt upgrade upgraded the unrelated package" foo 2.0
if apt2 install docker.io; then pass "apt install docker.io is a no-op"; else fail "apt install docker.io is a no-op"; cat "$W/out"; fi
# the guard can be replaced by a newer guard (an upgrade is not a removal)
if dp -i "$W"/guard-debs/thothdock-engine-guard_*.deb; then pass "guard reinstall"; else fail "guard reinstall"; cat "$W/out"; fi

# ---- part 4: the app's registration converges from any starting point ----
# EngineGuard.install() runs "dpkg -i ./*.deb || dpkg -i ./*.deb" in the guest.
R3="$W/root3"
mkdir -p "$R3/var/lib/dpkg" "$R3/inst"
: > "$R3/var/lib/dpkg/status"
dp3() { dpkg --admindir="$R3/var/lib/dpkg" --instdir="$R3/inst" --force-not-root --force-script-chrootless --no-triggers --log=/dev/null "$@" > "$W/out" 2>&1; }
installed3() { dpkg-query --admindir="$R3/var/lib/dpkg" -W -f='${Version}' "$1" 2>/dev/null || true; }
converge() { cd "$W/guard-debs" && { dp3 -i ./*.deb || dp3 -i ./*.deb; }; rc=$?; cd "$W"; return $rc; }
# a guest that already holds a stock engine
for p in $PROTECTED; do dp3 -i "$W/pool/${p}_26.1.5-1_all.deb" || { fail "stock $p into the migration root"; cat "$W/out"; }; done
if converge; then pass "registration replaces a stock engine already installed"; else fail "registration replaces a stock engine already installed"; cat "$W/out"; fi
for p in $PROTECTED; do
    [ "$(installed3 "$p")" = 9999:1.0+thothdock.1 ] && pass "migration: $p is the placeholder" || fail "migration: $p is '$(installed3 "$p")'"
done
[ "$(installed3 thothdock-engine-guard)" = 1.0+thothdock.3 ] && pass "migration: guard installed" || fail "migration: guard is '$(installed3 thothdock-engine-guard)'"
# a guest with the previous guard (no Depends/Conflicts): the new guard replaces it
R3="$W/root4"; mkdir -p "$R3/var/lib/dpkg" "$R3/inst"; : > "$R3/var/lib/dpkg/status"
mkdeb thothdock-engine-guard 1.0+thothdock.2
for p in $PROTECTED; do dp3 -i "$W/guard-debs/${p}_1.0+thothdock.1_all.deb" || fail "placeholder $p into the upgrade root"; done
dp3 -i "$W/pool/thothdock-engine-guard_1.0+thothdock.2_all.deb" || { fail "old guard into the upgrade root"; cat "$W/out"; }
if converge; then pass "the new guard upgrades the previous guard"; else fail "the new guard upgrades the previous guard"; cat "$W/out"; fi
[ "$(installed3 thothdock-engine-guard)" = 1.0+thothdock.3 ] && pass "upgrade: guard is thothdock.3" || fail "upgrade: guard is '$(installed3 thothdock-engine-guard)'"

if [ "$fails" -eq 0 ]; then echo "engine-guard: all passed"; else echo "engine-guard: $fails FAILED"; exit 1; fi
