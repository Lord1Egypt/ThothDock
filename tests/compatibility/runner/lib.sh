# Shared helpers for tests/compatibility/workloads/*/test.sh (POSIX sh, also
# Android's mksh/toybox). Sourced by runner/run.sh; never run directly.
#
# The caller puts a dk program on PATH (the docker CLI pinned to the engine
# under test; it must never fall back to a default context) and COMPAT_ID,
# a short run tag that makes every container, network and volume name
# unique and disposable: compat-$COMPAT_ID-*.

N=compat-$COMPAT_ID-net
fails=0
passes=0

ok() {
    if [ "$1" = 0 ]; then
        echo "PASS $2"; passes=$((passes + 1))
    else
        echo "FAIL $2"; fails=$((fails + 1))
    fi
}
note() { echo "NOTE $*"; }
# unsupported REASON: the workload needs something ThothDock does not provide
# by design; the test still checks that it fails cleanly.
unsupported() { echo "UNSUPPORTED $*"; }
name() { echo "compat-$COMPAT_ID-$1"; }

# wait_for SECONDS COMMAND...: retry every 0.5 s until COMMAND succeeds.
wait_for() {
    _limit=$(($1 * 2)); shift
    _i=0
    while [ $_i -lt $_limit ]; do
        "$@" >/dev/null 2>&1 && return 0
        sleep 0.5; _i=$((_i + 1))
    done
    return 1
}

running() { [ "$(dk inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }
exit_code() { dk inspect -f '{{.State.ExitCode}}' "$1" 2>/dev/null; }

# A container on the test's network; extra docker run options first.
run_bg() { _n=$1; shift; dk run -d --restart no --network "$N" --name "$(name "$_n")" "$@" >/dev/null; }
x() { _n=$1; shift; dk exec "$(name "$_n")" "$@"; }
xi() { _n=$1; shift; dk exec -i "$(name "$_n")" "$@"; }

# Everything this test created, removed by name only.
cleanup() {
    for _c in $(dk ps -a --format '{{.Names}}' | grep "^compat-$COMPAT_ID-"); do
        dk container remove --force "$_c" >/dev/null 2>&1
    done
    for _v in $(dk volume ls --format '{{.Name}}' | grep "^compat-$COMPAT_ID-"); do
        dk volume remove "$_v" >/dev/null 2>&1
    done
    dk network remove "$N" >/dev/null 2>&1
    return 0
}

setup() {
    cleanup
    dk network create "$N" >/dev/null
}

# logs_on_fail NAME: show the tail of a container's log after a failure.
logs_on_fail() { [ "$fails" = 0 ] || dk logs --tail 15 "$(name "$1")" 2>&1 | sed 's/^/    | /'; }

# no_leftovers: remove the test's containers, then check that no process of
# theirs survives (a container's PRoot argv carries its full id).
no_leftovers() {
    _ids=$(dk ps -aq --no-trunc --filter "name=compat-$COMPAT_ID-")
    cleanup
    sleep 1
    _left=0
    for _id in $_ids; do
        _left=$((_left + $(ps -A -o args 2>/dev/null | grep -c "[c]ontainers/$_id/")))
    done
    ok "$([ "$_left" = 0 ]; echo $?)" "cleanup leaves no container processes ($_left)"
}

finish() {
    echo "RESULT pass=$passes fail=$fails"
    [ "$fails" = 0 ]
}
