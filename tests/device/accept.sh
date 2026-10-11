#!/bin/bash
# ThothDock device acceptance, executed from inside the ThothTerm terminal on
# the phone (a real PTY, the bundled stock Docker CLI, the app-context daemon).
#
#   bash accept.sh [LOGFILE]        # default $HOME/accept.log; exit 1 on any FAIL
#
# Needs network (pulls alpine and debian) and the Engine Guard packages.
LOG=${1:-$HOME/accept.log}; : > $LOG
pass=0; fail=0
t() { # t "name" expected_rc expected_substring cmd...
  name="$1"; erc="$2"; sub="$3"; shift 3
  echo "\$ $*" | tee -a $LOG
  out=$("$@" 2>&1); rc=$?
  echo "$out" | tail -8 | cut -c1-200 | tee -a $LOG
  echo "[exit $rc]" | tee -a $LOG
  if [ "$rc" = "$erc" ] && echo "$out" | grep -q -- "$sub"; then echo "PASS: $name" | tee -a $LOG; pass=$((pass+1)); else echo "FAIL: $name" | tee -a $LOG; fail=$((fail+1)); fi
}
section() { echo | tee -a $LOG; echo "######## $*" | tee -a $LOG; }
SRV='while true; do printf "HTTP/1.0 200 OK\r\nContent-Length: 25\r\n\r\nhello-from-thothdock-web\n" | nc -l -p 8080; done'

section CLI
t "docker version (ThothDock server)" 0 "Server: ThothDock" docker version
t "docker info" 0 "Storage Driver: thothdock-copy" docker info
t "docker ps" 0 "CONTAINER ID" docker ps
t "docker images" 0 "IMAGE" docker images
t "docker pull alpine" 0 "Status:" docker pull alpine
t "docker pull debian" 0 "Status:" docker pull debian
t "run --rm alpine echo" 0 "hello" docker run --rm alpine echo hello
t "run --rm alpine uname -m" 0 "aarch64" docker run --rm alpine uname -m

section EXEC
docker rm -f golden-exec >/dev/null 2>&1
docker run -d --name golden-exec alpine sleep 300 >/dev/null
t "exec echo" 0 "hello" docker exec golden-exec echo hello
t "exec sh -c 'pwd; id'" 0 "uid=0(root)" docker exec golden-exec sh -c 'pwd; id'
t "exec exit code" 42 "" docker exec golden-exec sh -c 'exit 42'
t "exec env/workdir/user" 0 "bar /etc 65534" docker exec -e FOO=bar -w /etc -u nobody golden-exec sh -c 'echo $FOO $PWD $(id -u)'
t "exec -i stdin" 0 "PIPED" sh -c 'echo piped | docker exec -i golden-exec tr a-z A-Z'
t "exec stderr" 0 "to-err" sh -c 'docker exec golden-exec sh -c "echo to-err >&2" 2>&1 >/dev/null'
t "exec shares the container filesystem" 0 "shared" sh -c 'docker exec golden-exec sh -c "echo shared > /tmp/s"; docker exec golden-exec cat /tmp/s'
t "exec missing command -> 126" 126 "executable file not found" docker exec golden-exec nosuchcmd
docker rm -f golden-exec >/dev/null 2>&1
t "exec in stopped container refused" 1 "No such container" docker exec golden-exec echo hi

section VOLUMES
docker volume rm goldenvol >/dev/null 2>&1
t "volume create" 0 "goldenvol" docker volume create goldenvol
t "volume write" 0 "" docker run --rm -v goldenvol:/data alpine sh -c 'echo persistent > /data/test.txt'
t "volume read from a new container" 0 "persistent" docker run --rm -v goldenvol:/data alpine cat /data/test.txt
t "volume via --mount" 0 "persistent" docker run --rm --mount type=volume,src=goldenvol,dst=/other alpine cat /other/test.txt
t "volume ls" 0 "goldenvol" docker volume ls
t "volume inspect" 0 "local" docker volume inspect goldenvol --format '{{.Driver}} {{.Mountpoint}}'
docker run -d --name vol-holder -v goldenvol:/d alpine sleep 120 >/dev/null
t "volume rm refused while in use" 1 "volume is in use" docker volume rm goldenvol
t "volume prune keeps the in-use one" 0 "" sh -c 'docker volume prune -f >/dev/null; docker volume ls -q | grep -x goldenvol'
docker rm -f vol-holder >/dev/null 2>&1
t "traversal volume name refused" 1 "invalid characters" docker volume create '../escape'
t "volume survives container removal" 0 "persistent" docker run --rm -v goldenvol:/data alpine cat /data/test.txt
t "volume rm when unused" 0 "goldenvol" docker volume rm goldenvol

section PORTS
docker rm -f golden-web golden-web2 >/dev/null 2>&1
docker run -d --name golden-web -p 18080:8080 alpine sh -c "$SRV" >/dev/null; sleep 3
t "docker ps shows the mapping" 0 "127.0.0.1:18080->8080/tcp" docker ps --format '{{.Ports}}'
t "docker port" 0 "127.0.0.1:18080" docker port golden-web
t "inspect Ports" 0 '"HostIp":"127.0.0.1"' docker inspect golden-web --format '{{json .NetworkSettings.Ports}}'
if command -v curl >/dev/null; then t "curl 127.0.0.1:18080" 0 "hello-from-thothdock-web" curl -s --max-time 5 http://127.0.0.1:18080/; else t "wget via a container" 0 "hello-from-thothdock-web" docker run --rm alpine wget -qO- http://127.0.0.1:18080/; fi
t "port collision refused" 125 "port is already allocated" docker run -d --name golden-web2 -p 18080:8080 alpine sleep 5
docker rm -f golden-web2 >/dev/null 2>&1
t "UDP refused" 125 "only TCP" docker run --rm -p 5353:53/udp alpine true
t "LAN publish refused" 125 "127.0.0.1 only" docker run --rm -p 0.0.0.0:19000:9000 alpine true
docker stop -t 1 golden-web >/dev/null
t "listener gone after stop" 0 "gone" sh -c 'awk "NR>1 && \$4==\"0A\" && \$2 ~ /:46A0\$/" /proc/net/tcp | grep -q . && echo STILL-LISTENING || echo gone'
docker rm -f golden-web >/dev/null 2>&1

section ENGINE GUARD
t "guard placeholders installed" 0 "protected" sh -c 'thothdock doctor --guard | grep -m1 "docker.io"'
t "doctor --guard: no WARN/FAIL" 0 "no problems" sh -c 'thothdock doctor --guard | grep -E "WARN|FAIL" || echo no problems'
t "apt update" 0 "" sudo -n apt-get update -q
t "apt full-upgrade" 0 "" sudo -n apt-get -y full-upgrade
t "apt install engine packages is a no-op" 0 "0 newly installed" sudo -n apt-get install -y docker.io containerd runc docker-ce moby-engine
t "forced downgrade to the real engine refused" 100 "intentionally disabled" sudo -n apt-get install -y --allow-downgrades docker.io=26.1.5+dfsg1-9+deb13u1
t "dockerd absent" 0 "absent" sh -c 'command -v dockerd containerd runc >/dev/null && echo PRESENT || echo absent'
t "docker CLI still works after full-upgrade" 0 "Server: ThothDock" docker version
t "ThothDock still runs containers" 0 "still-works" docker run --rm alpine echo still-works
t "docker-cli package (CLI only) is allowed" 0 "docker-cli" sudo -n apt-get install -y docker-cli
t "bundled CLI still first in PATH" 0 "/usr/local/bin/docker" sh -c 'command -v docker'
t "doctor --guard after CLI install" 0 "PASS" sh -c 'thothdock doctor --guard | grep -m1 PASS'

section PACKAGE MANAGERS IN CONTAINERS
t "alpine apk + curl -I https" 0 "HTTP/2 200" docker run --rm alpine sh -c 'apk add --no-cache curl >/dev/null && curl -I https://example.com'
t "debian apt + curl -I https" 0 "HTTP/2 200" docker run --rm debian sh -c 'apt-get update >/dev/null && apt-get install -y curl >/dev/null && curl -I https://example.com'

section INSPECT, RESTART, PRUNE, TTY
docker rm -f rc-ins >/dev/null 2>&1
docker run -d --name rc-ins -e FOO=bar alpine sleep 300 >/dev/null
t "inspect state and env" 0 "running FOO=bar" sh -c 'echo $(docker inspect rc-ins --format "{{.State.Status}}") $(docker inspect rc-ins --format "{{range .Config.Env}}{{println .}}{{end}}" | grep FOO)'
t "docker stats is refused, never faked" 1 "does not implement" docker stats --no-stream rc-ins
t "restart" 0 "rc-ins" docker restart -t 1 rc-ins
t "an explicit restart is not a policy restart (Docker keeps RestartCount at 0)" 0 "0" docker inspect rc-ins --format '{{.RestartCount}}'
t "stop" 0 "rc-ins" docker stop -t 1 rc-ins
t "container prune removes the stopped one" 0 "Deleted Containers" docker container prune -f
t "interactive TTY" 0 "interactive-ok" docker run --rm -it alpine sh -c 'tty >/dev/null && echo interactive-ok'
docker rm -f loop >/dev/null 2>&1
docker run -d --name loop alpine sh -c 'while true; do sleep 1; done' >/dev/null; sleep 2
docker stop -t 3 loop >/dev/null
t "SIGTERM'd shell loop exits 143, not 0" 0 "143" docker inspect loop --format '{{.State.ExitCode}}'
docker rm -f loop >/dev/null 2>&1

section CONTAINER BASICS
docker rm -f ca cb >/dev/null 2>&1
docker create --name ca alpine sh -c 'echo ca-wrote > /etc/motd; cat /etc/motd' >/dev/null
docker create --name cb alpine cat /etc/motd >/dev/null
t "two containers: ca writes" 0 "ca-wrote" docker start -a ca
t "two containers: cb unaffected" 0 "Welcome to Alpine" docker start -a cb
docker rm -f ca cb >/dev/null 2>&1
t "exit code propagation" 42 "" docker run --rm alpine sh -c 'exit 42'
t "missing command -> 127" 127 "executable file not found" docker run --rm alpine nosuchcmd
t "stdin" 0 "PIPED" sh -c 'echo piped | docker run --rm -i alpine tr a-z A-Z'
t "memory limit refused" 125 "does not support memory limits" docker run --rm --memory 64m alpine true
docker rm -f ticker >/dev/null 2>&1
docker run -d --name ticker alpine sh -c 'trap "echo got-TERM; exit 0" TERM; i=0; while true; do echo tick $i; i=$((i+1)); sleep 1 & wait; done' >/dev/null; sleep 3
t "logs" 0 "tick 1" docker logs ticker
t "stop (graceful)" 0 "ticker" docker stop ticker
t "logs show TERM handled" 0 "got-TERM" docker logs ticker
t "start again" 0 "ticker" docker start ticker
sleep 1
t "kill" 0 "ticker" docker kill ticker
t "wait -> 137" 0 "137" docker wait ticker
t "rm" 0 "ticker" docker rm ticker
echo "RESULT pass=$pass fail=$fail" | tee -a $LOG
