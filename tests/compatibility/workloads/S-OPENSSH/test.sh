# S-OPENSSH: OpenSSH server and client from Alpine's packages, disposable
# key and account, reachable only inside the test network: key
# authentication, command execution, PTY allocation, SFTP upload/download
# with SHA-256 check, signal propagation and session cleanup.
# timeout: 360
IMG=alpine:3.22
dk pull -q $IMG >/dev/null
setup
K=$(name keys); dk volume create "$K" >/dev/null
dk run --rm -v "$K":/k $IMG sh -c 'apk add --no-cache openssh-keygen >/dev/null && ssh-keygen -q -t ed25519 -N "" -f /k/id && ssh-keygen -q -t ed25519 -N "" -f /k/host && cp /k/id.pub /k/authorized_keys && chmod 644 /k/*'
run_bg sshd -v "$K":/k $IMG sh -c '
    apk add --no-cache openssh-server >/dev/null &&
    adduser -D -s /bin/sh tester && echo "tester:*" | chpasswd -e >/dev/null 2>&1; passwd -u tester >/dev/null 2>&1
    mkdir -p /home/tester/.ssh /var/empty && cp /k/authorized_keys /home/tester/.ssh/ && chown -R tester /home/tester/.ssh && chmod 700 /home/tester/.ssh && chmod 600 /home/tester/.ssh/authorized_keys
    cp /k/host /tmp/hostkey && chmod 600 /tmp/hostkey
    exec /usr/sbin/sshd -D -e -p 2222 -h /tmp/hostkey -o ListenAddress=0.0.0.0 -o PasswordAuthentication=no -o UsePAM=no -o PidFile=none -o StrictModes=no'
S=$(name sshd)
cli() { dk run --rm --network "$N" -v "$K":/k $IMG sh -c "apk add --no-cache openssh-client openssh-sftp-server >/dev/null 2>&1; $*"; }
SSH="ssh -i /k/id -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o ConnectTimeout=10 tester@$S"
wait_for 90 sh -c "dk run --rm --network $N -v $K:/k $IMG sh -c 'apk add --no-cache openssh-client >/dev/null 2>&1; ssh -i /k/id -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o ConnectTimeout=5 tester@$S true'"
ok $? "key-authenticated login works (sshd privilege separation under PRoot)"
[ "$fails" = 0 ] || { logs_on_fail sshd; no_leftovers; return 0; }
ok "$([ "$(cli "$SSH 'echo hi; id -un'" | tr '\n' ' ')" = "hi tester " ]; echo $?)" "remote command output and user"
ok "$([ "$(cli "$SSH 'exit 7'; echo \$?")" = 7 ]; echo $?)" "remote exit status propagates"
ok "$(cli "$SSH -tt 'tty; echo \$TERM'" | tr -d '\r' | grep -q '^/dev/pts/'; echo $?)" "PTY allocation (-tt): the remote has a tty"
ok "$([ "$(cli "echo piped | $SSH 'tr a-z A-Z'" )" = PIPED ]; echo $?)" "stdin piped through the session"
cli "head -c 2000000 /dev/urandom > /tmp/up && sha256sum /tmp/up | cut -d' ' -f1 > /tmp/up.sum && printf 'put /tmp/up /home/tester/up\nget /home/tester/up /tmp/down\n' | sftp -q -i /k/id -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes tester@$S >/dev/null && sha256sum /tmp/down | cut -d' ' -f1 | cmp - /tmp/up.sum && echo sftp-roundtrip-ok" > "$TMPD/sftp.out" 2>&1
ok "$(grep -q sftp-roundtrip-ok "$TMPD/sftp.out"; echo $?)" "SFTP upload and download of 2 MB, SHA-256 equal"
# Signal propagation and cleanup: kill the local ssh, the remote command must go.
cli "$SSH -tt 'echo \$\$ > /tmp/rpid; exec sleep 300' & sleep 4; kill -TERM \$! ; sleep 3; echo killed" >/dev/null 2>&1
sleep 3
left=$(dk exec "$S" sh -c 'for d in /proc/[0-9]*; do [ "$(cat $d/comm 2>/dev/null)" = sleep ] && echo x; done' | wc -l)
ok "$([ "$left" = 0 ]; echo $?)" "a killed client's remote command does not outlive its session ($left sleep left)"
sess=$(dk exec "$S" sh -c 'for d in /proc/[0-9]*; do case "$(cat $d/cmdline 2>/dev/null | tr "\0" " ")" in "sshd: tester"*) echo x;; esac; done' | wc -l)
ok "$([ "$sess" = 0 ]; echo $?)" "no session processes remain ($sess)"
dk stop -t 15 "$S" >/dev/null
ok "$(c=$(exit_code "$S"); [ "$c" = 0 ] || [ "$c" = 255 ] || [ "$c" = 143 ]; echo $?)" "sshd stops (exit $(exit_code "$S"))"
logs_on_fail sshd
no_leftovers
