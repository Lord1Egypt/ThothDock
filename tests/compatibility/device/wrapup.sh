. /data/local/tmp/thoth-sysvipc/qa/env.sh
for c in $(dk ps -aq); do dk container remove --force $c >/dev/null 2>&1; done
for n in $(dk network ls --format '{{.Name}}' | grep '^compat-'); do dk network remove $n >/dev/null 2>&1; done
for v in $(dk volume ls -q); do dk volume remove $v >/dev/null 2>&1; done
echo "left: $(dk ps -aq | wc -l) containers, $(dk volume ls -q | wc -l) volumes"
cp $Q/daemon.log $Q/ev/daemon.log 2>/dev/null
# evidence is streamed out from the host: adb exec-out "run-as PKG tar cf - -C files/tdqa ev daemon.log"
for p in $(ps -A -o PID,ARGS | grep "[l]ibthothdock.so serve --root $Q" | awk '{print $1}'); do kill $p; done
sleep 3
echo "engine processes left: $(ps -A -o ARGS | grep -c "[l]ibthothdock.so serve --root $Q")"
