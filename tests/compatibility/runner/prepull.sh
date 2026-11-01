#!/bin/sh
# Pull the suite's images through a public mirror of Docker Hub and tag them
# with their original names, so the tests (which name Docker Hub images) find
# them locally and never spend the anonymous pull quota (100 per hour).
#
#   COMPAT_DOCKER_HOST=unix://... [COMPAT_MIRROR=mirror.gcr.io] runner/prepull.sh IMAGE...
#
# The mirror serves the same manifests, so the digests are the Docker Hub
# ones; each tag is checked against matrix.json's digest when it is listed.
set -u
: "${COMPAT_DOCKER_HOST:?set COMPAT_DOCKER_HOST=unix:///.../thothdock.sock}"
MIRROR=${COMPAT_MIRROR:-mirror.gcr.io}
CLI=${COMPAT_DOCKER_CLI:-docker}
HERE=$(cd "$(dirname "$0")" && pwd)
d() { DOCKER_CONTEXT= DOCKER_HOST= "$CLI" -H "$COMPAT_DOCKER_HOST" "$@"; }
for img in "$@"; do
    case "$img" in */*) src=$img ;; *) src=library/$img ;; esac
    if d image inspect "$img" >/dev/null 2>&1; then echo "have   $img"; continue; fi
    if d pull -q "$MIRROR/$src" >/dev/null 2>&1 && d tag "$MIRROR/$src" "$img"; then
        want=$(sed -n "/\"image\": \"$(echo "$img" | sed 's/[\/.]/\\&/g')\"/,/\"architectures\"/s/.*\"digest\": \"\(sha256:[0-9a-f]*\)\".*/\1/p" "$HERE/../matrix.json" | head -1)
        have=$(d image inspect -f '{{index .RepoDigests 0}}' "$MIRROR/$src" 2>/dev/null | sed 's/.*@//')
        echo "pulled $img ($have)${want:+ matrix=$([ "$want" = "$have" ] && echo same || echo DIFFERENT)}"
    else
        echo "FAILED $img via $MIRROR" >&2
    fi
done
