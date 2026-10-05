#!/bin/sh
# apt DPkg::Pre-Install-Pkgs hook (protocol version 3, fd 3): refuse to
# install or upgrade a protected Docker Engine package to anything but the
# ThothDock placeholder. Lines after the blank line have the form
#   name old-version compare new-version action [path]
# and apt aborts the dpkg run if this hook exits non-zero.
PROTECTED="@PROTECTED@"
bad=
in_list=
while IFS= read -r line <&3; do
    if [ -z "$in_list" ]; then
        [ -z "$line" ] && in_list=1
        continue
    fi
    set -- $line
    name=$1 new=$4 action=$5
    case "$action" in '**CONFIGURE**'|'**REMOVE**') continue ;; esac
    for p in $PROTECTED; do
        if [ "$name" = "$p" ]; then
            case "$new" in 9999:*) ;; *) bad="$bad $name($new)" ;; esac
        fi
    done
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
