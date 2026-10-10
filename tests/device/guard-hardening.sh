#!/bin/bash
# Engine Guard hardening, run in a Debian-family ThothTerm guest on a phone
# (inside the terminal with sudo, or as guest root through a harness).
#
#   bash guard-hardening.sh [LOGFILE]     # default $HOME/guard-hardening.log; exit 1 on any FAIL
#
# Use a disposable guest: it runs real apt-get and dpkg, including attempts to
# install a stock Docker Engine (all of which must be refused) and downloads
# the real Debian packages for them (about 100 MB). Needs the network.
LOG=${1:-$HOME/guard-hardening.log}; : > "$LOG"
SUDO=; [ "$(id -u)" = 0 ] || SUDO=sudo
export DEBIAN_FRONTEND=noninteractive
APT="$SUDO apt-get -y"
pass=0; fail=0
t() { # t "name" expected_rc expected_substring cmd...   (substring "" = any output)
  name="$1"; erc="$2"; sub="$3"; shift 3
  echo "\$ $*" >> "$LOG"
  out=$("$@" 2>&1); rc=$?
  echo "$out" | tail -6 | cut -c1-200 >> "$LOG"; echo "[exit $rc]" >> "$LOG"
  if [ "$rc" = "$erc" ] && echo "$out" | grep -q -- "$sub"; then echo "PASS: $name"; echo "PASS: $name" >> "$LOG"; pass=$((pass+1))
  else echo "FAIL: $name (exit $rc)"; echo "$out" | tail -4 | cut -c1-160; echo "FAIL: $name" >> "$LOG"; fail=$((fail+1)); fi
}
section() { echo; echo "######## $*"; echo "######## $*" >> "$LOG"; }
managed() { sha256sum /usr/local/bin/docker /usr/local/bin/thothdock | cut -c1-64 | tr '\n' ' '; sha256sum /usr/bin/docker 2>/dev/null | cut -c1-64; }
realbins() { ls /usr/bin/dockerd /usr/sbin/dockerd /usr/bin/containerd /usr/bin/containerd-shim* /usr/bin/runc /usr/sbin/runc 2>/dev/null | wc -l; }
engineprocs() { n=$(ps -eo comm | grep -cE '^(dockerd|containerd|runc|containerd-shim)'); echo "$n"; }
placeholders_ok() {
  for p in docker.io docker-ce docker-engine moby-engine containerd containerd.io runc; do
    [ "$(dpkg-query -W -f='${Version}' "$p" 2>/dev/null)" = "9999:1.0+thothdock.1" ] || return 1
  done
  dpkg-query -W -f='${Status}' thothdock-engine-guard 2>/dev/null | grep -q 'install ok installed' && echo placeholders-intact
}
CONTAINERS=$(docker ps -aq | wc -l); IMAGES=$(docker images -q | wc -l); VOLUMES=$(docker volume ls -q | wc -l)
BEFORE=$(managed)
W=$(mktemp -d); trap 'rm -rf "$W"' EXIT

section "ordinary apt"
t "apt-get update" 0 "" $SUDO apt-get update
t "apt-get upgrade" 0 "" $APT upgrade
t "apt-get full-upgrade" 0 "" $APT full-upgrade
t "install an unrelated package" 0 "" $APT install tree
t "unrelated package works" 0 "tree v" tree --version
t "remove an unrelated package" 0 "" $APT remove tree
t "docker-cli and docker-compose stay installable" 0 "" $APT install docker-cli docker-compose
t "the placeholders are untouched by ordinary apt" 0 "placeholders-intact" placeholders_ok

section "stock engine names"
for p in docker.io docker-ce containerd containerd.io runc moby-engine docker-engine; do
  t "apt install $p is a no-op on the placeholder" 0 "already the newest version (9999:" $APT install "$p"
done
for p in docker docker-ce-cli docker-compose-plugin; do
  t "apt install $p (not in Debian) fails honestly" 100 "E: " $APT install "$p"
done
RV=$(apt-cache madison runc | awk 'NR==1{print $3}'); DV=$(apt-cache madison docker.io | awk 'NR==1{print $3}'); CV=$(apt-cache madison containerd | awk 'NR==1{print $3}')
t "explicit stock docker.io version is refused" 100 "E: " $APT --allow-downgrades install "docker.io=$DV"
t "explicit stock runc version is refused" 100 "E: " $APT --allow-downgrades install "runc=$RV"
t "explicit stock containerd version is refused" 100 "E: " $APT --allow-downgrades install "containerd=$CV"
t "reinstall of a placeholder cannot fetch a stock engine" 0 "" $APT --reinstall install docker.io

section "removal"
for p in runc docker.io containerd.io thothdock-engine-guard; do
  t "apt remove $p is refused" 100 "Refusing to remove the ThothDock Docker Engine placeholder" $APT remove "$p"
  t "apt purge $p is refused" 100 "Refusing to remove the ThothDock Docker Engine placeholder" $APT purge "$p"
done
t "podman-docker (Conflicts: docker.io) is refused" 100 "Refusing to remove the ThothDock Docker Engine placeholder" $APT install podman-docker
t "the placeholders survived every removal attempt" 0 "placeholders-intact" placeholders_ok

section "direct dpkg (apt's hook does not run)"
mkdir -p "$W/dl" && ( cd "$W/dl" && apt-get download "runc=$RV" "containerd=$CV" "docker.io=$DV" >/dev/null 2>&1 )
for f in "$W"/dl/*.deb; do
  t "dpkg -i $(basename "$f" | cut -d_ -f1) (stock) is refused before unpacking" 1 "conflict" $SUDO dpkg -i --force-depends "$f"
done
t "dpkg -r runc is refused" 1 "dependency problems" $SUDO dpkg -r runc
t "dpkg -P containerd is refused" 1 "dependency problems" $SUDO dpkg -P containerd
t "no stock engine binary landed" 0 "^0$" realbins
mkdir -p "$W/p/DEBIAN" "$W/p/usr/local/bin"; chmod 755 "$W/p" "$W/p/DEBIAN" "$W/p/usr" "$W/p/usr/local" "$W/p/usr/local/bin"
printf 'Package: shim-test\nVersion: 1\nArchitecture: all\nMaintainer: t <t@t.invalid>\nDescription: t\n' > "$W/p/DEBIAN/control"
printf '#!/bin/sh\necho SHIM\n' > "$W/p/usr/local/bin/docker"; cp "$W/p/usr/local/bin/docker" "$W/p/usr/local/bin/thothdock"; chmod 755 "$W/p/usr/local/bin/"*
dpkg-deb --root-owner-group --build "$W/p" "$W/shim.deb" >/dev/null 2>&1
t "a package cannot replace the managed CLI or engine tool" 1 "Operation not permitted" $SUDO dpkg -i "$W/shim.deb"

section "the engine afterwards"
t "managed CLI and engine tool unchanged" 0 "$BEFORE" managed
t "placeholders and guard intact" 0 "placeholders-intact" placeholders_ok
t "no stock engine process in the guest" 0 "^0$" engineprocs
t "docker version" 0 "Server: ThothDock" docker version
t "docker info" 0 "Storage Driver: thothdock-copy" docker info
t "docker ps" 0 "CONTAINER ID" docker ps
t "docker compose version" 0 "Docker Compose version" docker compose version
t "containers, images and volumes intact" 0 "^$CONTAINERS/$IMAGES/$VOLUMES$" sh -c 'echo $(docker ps -aq | wc -l)/$(docker images -q | wc -l)/$(docker volume ls -q | wc -l)'
t "doctor --guard: no WARN/FAIL" 0 "no problems" sh -c 'thothdock doctor --guard | grep -E "WARN|FAIL" || echo no problems'

echo; echo "guard hardening: $pass passed, $fail failed (log: $LOG)"
[ "$fail" -eq 0 ]
