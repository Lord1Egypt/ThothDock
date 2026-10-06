#!/bin/sh
# apt DPkg::Pre-Install-Pkgs hook (protocol version 3, fd 3): refuse to
# install or upgrade a protected Docker Engine package to anything but the
# ThothDock placeholder. apt writes its configuration, a blank line, then
# one record per package, nine fields (apt 2.x, captured from a real run):
#   name oldversion oldarch oldmulti compare newversion newarch newmulti action
# where action is the .deb path, or **CONFIGURE** / **REMOVE**. For example
#   docker.io 9999:1.0+thothdock.1 all none > 26.1.5-1 all none /var/cache/apt/archives/docker.io_26.1.5-1_all.deb
# apt aborts the dpkg run if this hook exits non-zero. Fail closed: a record
# of a protected package that cannot be read is refused, and so is a stream
# that is not protocol 3.
PROTECTED="@PROTECTED@"
bad=
in_list=
first=1
while IFS= read -r line <&3; do
    if [ -n "$first" ]; then
        first=
        if [ "$line" != "VERSION 3" ]; then
            echo "E: ThothDock Engine Guard: unsupported apt hook protocol \"$line\"" >&2
            exit 1
        fi
        continue
    fi
    if [ -z "$in_list" ]; then
        [ -z "$line" ] && in_list=1
        continue
    fi
    set -f
    set -- $line
    set +f
    [ $# -eq 0 ] && continue
    name=${1%%:*}
    protected=
    for p in $PROTECTED; do
        [ "$name" = "$p" ] && protected=1
    done
    [ -z "$protected" ] && continue
    if [ $# -lt 9 ]; then
        bad="$bad $name(unreadable)"
        continue
    fi
    new=$6 action=$9
    case "$action" in '**CONFIGURE**'|'**REMOVE**') continue ;; esac
    case "$new" in 9999:*+thothdock.*) ;; *) bad="$bad $name($new)" ;; esac
done
if [ -n "$bad" ]; then
    {
        echo "E: Refusing to install the stock Docker Engine:$bad"
        echo "E: Docker Engine is provided by ThothDock. Stock dockerd/containerd/runc"
        echo "E: are intentionally disabled (they need kernel namespaces and cgroups)."
        echo "E: The Docker CLI is unaffected; update ThothDock through its app."
    } >&2
    exit 1
fi
exit 0
