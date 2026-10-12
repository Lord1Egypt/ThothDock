# I-DOOD: Docker-outside-of-Docker. The engine socket is bind-mounted into a
# docker:cli container, which then manages sibling containers. A container
# holding the socket controls the whole engine, so ThothDock refuses binds
# from its data root and needs --allow-bind for anything else; on Android
# the socket stays private (unsupported by design). Where the engine allows
# the socket's directory, the CLI inside must work end to end.
# timeout: 240
IMG=docker:29-cli
dk pull -q $IMG >/dev/null; dk pull -q busybox:1.37 >/dev/null
setup
sock=${COMPAT_DOCKER_HOST#unix://}
sdir=$(dirname "$sock")
out=$(dk run -d --network "$N" --name "$(name cli)" -v "$sdir":/run/engine -e DOCKER_HOST=unix:///run/engine/$(basename "$sock") $IMG sleep 300 2>&1)
if ! running "$(name cli)"; then
    unsupported "the engine socket cannot be mounted into a container: $(echo "$out" | tail -1)"
    ok "$(echo "$out" | grep -qiE 'allow-bind|data root|not allowed|refused'; echo $?)" "the bind is refused with a clear reason"
else
    ok "$(x cli docker version --format '{{.Server.Platform.Name}}' | grep -q ThothDock; echo $?)" "docker version inside the container reaches ThothDock"
    ok "$([ "$(x cli docker run --rm busybox:1.37 echo sibling)" = sibling ]; echo $?)" "a sibling container started from inside"
    ok "$(x cli docker ps --format '{{.Names}}' | grep -q "$(name cli)"; echo $?)" "docker ps inside lists the calling container"
fi
no_leftovers
