# I-DIND: the official docker:dind image runs a full dockerd, which needs
# kernel namespaces, cgroups, overlay mounts and iptables, none of which an
# unprivileged PRoot container has. Unsupported by design; the checks are
# that --privileged is refused clearly and that dockerd fails cleanly
# (container exits, no process left), never a hang.
# timeout: 240
IMG=docker:29-dind
dk pull -q $IMG >/dev/null
setup
out=$(dk run -d --privileged --network "$N" --name "$(name priv)" $IMG 2>&1)
ok "$(echo "$out" | grep -qi 'not support\|privileged'; echo $?)" "--privileged is refused with a clear error"
note "privileged: $(echo "$out" | tail -1)"
run_bg dind -e DOCKER_TLS_CERTDIR= $IMG
D=$(name dind)
# dockerd either serves (unexpected) or exits; give it 90 s.
served=1
wait_for 90 sh -c "dk exec $D docker -H unix:///var/run/docker.sock info >/dev/null 2>&1 || ! dk inspect -f '{{.State.Running}}' $D | grep -q true" && \
    dk exec "$D" docker -H unix:///var/run/docker.sock info >/dev/null 2>&1 && served=0
if [ $served = 0 ]; then
    note "dockerd answered inside the container: try a nested docker run"
    ok "$(dk exec "$D" docker run --rm busybox:1.37 echo nested | grep -q nested; echo $?)" "nested docker run"
else
    unsupported "dockerd cannot run without kernel namespaces, cgroups and mounts"
    ok "$(! running "$D"; echo $?)" "the dind container exits instead of hanging (exit $(exit_code "$D"))"
    note "dockerd said: $(dk logs "$D" 2>&1 | grep -iE 'level=(error|fatal)|failed to|permission|operation not permitted' | tail -3 | tr '\n' ' ' | cut -c1-400)"
fi
no_leftovers
