#!/bin/sh
# Run compatibility workloads against one ThothDock engine.
#
#   COMPAT_DOCKER_HOST=unix:///path/to/thothdock.sock \
#   [COMPAT_DOCKER_CLI=docker] [COMPAT_EVIDENCE=DIR] [COMPAT_PROBES=DIR] \
#       tests/compatibility/runner/run.sh TEST-ID...
#
# TEST-ID is a directory under workloads/ (e.g. G-ALPINE, A-POSTGRES), or
# "all" for every one in matrix.json order. Each test runs alone, with
# unique disposable names (compat-<run>-*), and is cut off after its
# timeout. Per test the evidence directory gets <ID>.log and one line in
# results.txt:
#
#   id=<ID> status=<PASS|FAIL|TIMEOUT> exit=<n> pass=<n> fail=<n> seconds=<n>
#
# runner/report.py turns results.txt files into matrix.json/csv/md.
# POSIX sh: runs on a Linux host and on Android under run-as (toybox).
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
: "${COMPAT_DOCKER_HOST:?set COMPAT_DOCKER_HOST=unix:///.../thothdock.sock (never a default context)}"
case "$COMPAT_DOCKER_HOST" in
unix://*) ;;
*) echo "run.sh: COMPAT_DOCKER_HOST must be a unix:// socket of a ThothDock engine" >&2; exit 2 ;;
esac
COMPAT_CLI_BIN=${COMPAT_DOCKER_CLI:-docker}
EVID=${COMPAT_EVIDENCE:-$ROOT/evidence/$(date +%Y%m%d-%H%M%S)}
export COMPAT_PROBES="${COMPAT_PROBES:-$ROOT/probes/bin}"
# Optional: the engine's --root, so tests can look for leftover temporary files.
export COMPAT_ENGINE_ROOT="${COMPAT_ENGINE_ROOT:-}"
mkdir -p "$EVID"
EVID=$(cd "$EVID" && pwd)

# dk: the CLI pinned to this engine, as a program (not a function) so that
# tests can use it inside sh -c as well.
mkdir -p "$EVID/bin"
cat > "$EVID/bin/dk" <<DK
#!/bin/sh
DOCKER_CONTEXT= DOCKER_HOST= exec "$COMPAT_CLI_BIN" -H "$COMPAT_DOCKER_HOST" "\$@"
DK
chmod 755 "$EVID/bin/dk"
PATH="$EVID/bin:$PATH"
export PATH

# The engine must be ThothDock; refuse anything else before running a test.
server=$(DOCKER_CONTEXT= DOCKER_HOST= "$COMPAT_CLI_BIN" -H "$COMPAT_DOCKER_HOST" version --format '{{.Server.Platform.Name}} {{.Server.Version}}' 2>/dev/null)
case "$server" in
*ThothDock*) ;;
*) echo "run.sh: $COMPAT_DOCKER_HOST is not a ThothDock engine ($server)" >&2; exit 2 ;;
esac
arch=$(DOCKER_CONTEXT= DOCKER_HOST= "$COMPAT_CLI_BIN" -H "$COMPAT_DOCKER_HOST" info --format '{{.Architecture}}' 2>/dev/null)
{
    echo "engine=$server"
    echo "arch=$arch"
    echo "kernel=$(uname -r)"
    echo "started=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "harness=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
} > "$EVID/environment.txt"

ids="$*"
if [ "$ids" = all ]; then
    ids=$(sed -n 's/.*"id": *"\([A-Z0-9-]*\)".*/\1/p' "$ROOT/matrix.json")
fi

for id in $ids; do
    t="$ROOT/workloads/$id/test.sh"
    if [ ! -f "$t" ]; then
        echo "id=$id status=NOT_TESTED exit=- pass=0 fail=0 seconds=0 note=no-test-script" >> "$EVID/results.txt"
        continue
    fi
    limit=$(sed -n 's/^# timeout: *\([0-9]*\).*/\1/p' "$t")
    limit=${limit:-600}
    run=$(date +%s | tail -c 6)$$
    start=$(date +%s)
    echo "== $id (timeout ${limit}s)"
    # A subshell with its own process group, so the timeout can end it whole.
    (
        COMPAT_ID=$(echo "$id" | tr 'A-Z' 'a-z')-$run
        TMPD=$EVID/tmp/$id
        mkdir -p "$TMPD"
        export COMPAT_ID TMPD
        cd "$ROOT/workloads/$id"
        . "$HERE/lib.sh"
        . ./test.sh
        cleanup
        finish
    ) > "$EVID/$id.log" 2>&1 &
    child=$!
    elapsed=0
    while kill -0 "$child" 2>/dev/null && [ $elapsed -lt "$limit" ]; do
        sleep 1; elapsed=$((elapsed + 1))
    done
    status=
    if kill -0 "$child" 2>/dev/null; then
        kill -TERM "$child" 2>/dev/null; sleep 2; kill -KILL "$child" 2>/dev/null
        status=TIMEOUT
    fi
    wait "$child"; code=$?
    seconds=$(( $(date +%s) - start ))
    p=$(grep -c '^PASS' "$EVID/$id.log"); f=$(grep -c '^FAIL' "$EVID/$id.log")
    if [ -z "$status" ]; then
        if [ "$f" = 0 ] && grep -q '^UNSUPPORTED' "$EVID/$id.log"; then status=UNSUPPORTED_BY_DESIGN
        elif [ "$code" = 0 ] && [ "$f" = 0 ] && [ "$p" -gt 0 ]; then status=PASS
        elif grep -q '^BLOCKED' "$EVID/$id.log"; then status=BLOCKED
        elif [ "$p" -gt 0 ] && [ "$f" -gt 0 ]; then status=PARTIAL
        else status=FAIL; fi
    fi
    echo "id=$id status=$status exit=$code pass=$p fail=$f seconds=$seconds" >> "$EVID/results.txt"
    echo "   $status (pass=$p fail=$f, ${seconds}s)"
    # A timed-out test may have left containers: remove them by name.
    if [ "$status" = TIMEOUT ]; then
        for c in $(DOCKER_CONTEXT= DOCKER_HOST= "$COMPAT_CLI_BIN" -H "$COMPAT_DOCKER_HOST" ps -a --format '{{.Names}}' | grep "^compat-$(echo "$id" | tr 'A-Z' 'a-z')-$run-"); do
            DOCKER_CONTEXT= DOCKER_HOST= "$COMPAT_CLI_BIN" -H "$COMPAT_DOCKER_HOST" container remove --force "$c" >/dev/null 2>&1
        done
    fi
done
echo "evidence: $EVID"
