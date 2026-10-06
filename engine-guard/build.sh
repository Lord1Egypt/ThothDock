#!/bin/sh
# Build ThothDock's Engine Guard packages (Debian/Ubuntu guests).
#
#   engine-guard/build.sh OUT_DIR
#
# ThothDock is the engine. These packages keep a stock Docker Engine from
# ever being installed in the guest:
#
#   * one empty placeholder per ENGINE package name, versioned
#     9999:1.0+thothdock.N -- an epoch no repository reaches, so apt sees the
#     placeholder as "already newest" and never replaces it; they contain no
#     files, so dockerd, containerd and runc stay absent;
#   * thothdock-engine-guard: an apt pin (priority 1001 on those versions), a
#     Pre-Install-Pkgs hook that refuses to install a protected name at any
#     other version (e.g. apt install --allow-downgrades), and a notice.
#
# The Docker CLI is NOT guarded: docker-cli / docker-ce-cli contain no daemon
# (verified against the Debian trixie packages) and may be updated freely.
#
# Output is reproducible: fixed timestamps, owner root:root, uncompressed (the placeholders are ~100 bytes; a compressor is a host-dependent byte source).
set -eu
# File and directory modes end up in the packages: do not inherit the caller's umask (a 002 build user
# makes drwxrwxr-x directories and a different package).
umask 022
OUT="${1:?usage: build.sh OUT_DIR}"
HERE="$(cd "$(dirname "$0")" && pwd)"
VERSION='9999:1.0+thothdock.1'   # the placeholders
GUARD_VERSION='1.0+thothdock.2' # the guard: bumped when its hook or pin changes, so dpkg replaces an older one
EPOCH="${SOURCE_DATE_EPOCH:-1790000000}"
export SOURCE_DATE_EPOCH="$EPOCH"
PROTECTED="docker.io docker-ce docker-engine moby-engine containerd containerd.io runc"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
rm -f "$OUT"/*.deb
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

build() { # dir -> deb
    chmod -R go-w "$1"
    ( cd "$1" && find . -exec touch -h -d "@$EPOCH" {} + )
    dpkg-deb --root-owner-group -Znone --build "$1" "$OUT/$2" >/dev/null
}

for p in $PROTECTED; do
    d="$WORK/$p"; mkdir -p "$d/DEBIAN"
    cat > "$d/DEBIAN/control" <<CTL
Package: $p
Version: $VERSION
Architecture: all
Section: admin
Priority: optional
Maintainer: ThothDock <noreply@users.noreply.github.com>
Description: Docker Engine placeholder -- the engine is provided by ThothDock
 This empty package stands in for the stock "$p" package. Docker Engine is
 provided by ThothDock, the userspace engine of this ThothTerm app; the real
 dockerd, containerd and runc are intentionally absent and must not be
 installed, because they cannot work without Linux kernel namespaces and
 cgroups. It carries an epoch no repository reaches, so "apt upgrade" never
 replaces it. The Docker CLI is unaffected.
CTL
    build "$d" "${p}_${VERSION#*:}_all.deb"
done

g="$WORK/thothdock-engine-guard"
mkdir -p "$g/DEBIAN" "$g/etc/apt/preferences.d" "$g/etc/apt/apt.conf.d" "$g/usr/lib/thothdock" "$g/usr/share/doc/thothdock-engine-guard"
cat > "$g/DEBIAN/control" <<CTL
Package: thothdock-engine-guard
Version: $GUARD_VERSION
Architecture: all
Section: admin
Priority: optional
Maintainer: ThothDock <noreply@users.noreply.github.com>
Description: Keeps a stock Docker Engine from replacing ThothDock
 Installs an apt pin and an apt hook that refuse any attempt to install the
 stock Docker Engine packages (docker.io, docker-ce, docker-engine,
 moby-engine, containerd, containerd.io, runc) in place of the ThothDock
 placeholders. The Docker CLI is not restricted.
CTL
cat > "$g/DEBIAN/postinst" <<'SH'
#!/bin/sh
set -e
if [ "$1" = configure ]; then
    echo "Docker Engine is provided by ThothDock." >&2
    echo "Stock dockerd/containerd/runc are intentionally disabled." >&2
fi
exit 0
SH
chmod 755 "$g/DEBIAN/postinst"
{
    echo "# Managed by thothdock-engine-guard. The stock Docker Engine packages stay"
    echo "# at the ThothDock placeholder versions; the Docker CLI is not pinned."
    echo "Package: $PROTECTED"
    echo "Pin: version 9999:*"
    echo "Pin-Priority: 1001"
} > "$g/etc/apt/preferences.d/thothdock-engine-guard"
cat > "$g/etc/apt/apt.conf.d/99thothdock-engine-guard" <<'CONF'
DPkg::Pre-Install-Pkgs { "/usr/lib/thothdock/engine-guard-hook"; };
DPkg::Tools::Options::/usr/lib/thothdock/engine-guard-hook::Version "3";
DPkg::Tools::Options::/usr/lib/thothdock/engine-guard-hook::InfoFD "3";
CONF
sed "s/@PROTECTED@/$PROTECTED/" "$HERE/engine-guard-hook.sh" > "$g/usr/lib/thothdock/engine-guard-hook"
chmod 755 "$g/usr/lib/thothdock/engine-guard-hook"
cp "$HERE/README.Debian" "$g/usr/share/doc/thothdock-engine-guard/README.Debian"
build "$g" "thothdock-engine-guard_${GUARD_VERSION}_all.deb"
( cd "$OUT" && sha256sum *.deb > SHA256SUMS )
cat "$OUT/SHA256SUMS"
