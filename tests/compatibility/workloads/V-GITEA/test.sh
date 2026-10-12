# V-GITEA: Gitea (Go, SQLite, git subprocesses, hard links, file
# permissions) with disposable data: install, admin user and repository
# through the API, git clone/commit/push/fetch over HTTP, restart and
# persistence. Forgejo's own registry is not reachable with plain Docker Hub
# tokens, and Gitea shares its code base.
# timeout: 420
IMG=gitea/gitea:1.24-rootless; CLI=alpine:3.22
dk pull -q $IMG >/dev/null; dk pull -q $CLI >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
run_bg git -e GITEA__security__INSTALL_LOCK=true -e GITEA__database__DB_TYPE=sqlite3 -e GITEA__server__HTTP_PORT=3000 \
    -e GITEA__server__ROOT_URL=http://git:3000/ -e GITEA__server__DISABLE_SSH=true -e GITEA__log__LEVEL=Info \
    -e GITEA__repository__DEFAULT_BRANCH=main -v "$V":/var/lib/gitea -v "$V":/etc/gitea $IMG
G=$(name git)
api() { dk run --rm --network "$N" $CLI sh -c "apk add --no-cache curl >/dev/null 2>&1; curl -s --max-time 20 -u tester:Compat-Passw0rd! -H 'Content-Type: application/json' $*"; }
wait_for 180 sh -c "dk run --rm --network $N $CLI wget -q -O /dev/null -T 3 http://$G:3000/api/healthz"
ok $? "Gitea started and its SQLite database initialised"
[ "$fails" = 0 ] || { logs_on_fail git; no_leftovers; return 0; }
dk exec "$G" gitea admin user create --admin --username tester --password 'Compat-Passw0rd!' --email t@test.invalid --must-change-password=false >/dev/null 2>&1
ok "$(api http://$G:3000/api/v1/user | grep -q '"login":"tester"'; echo $?)" "admin user created (password hashing) and authenticated over the API"
ok "$(api -X POST http://$G:3000/api/v1/user/repos -d '{"name":"demo","auto_init":true,"default_branch":"main"}' | grep -q '"name":"demo"'; echo $?)" "repository created (git init, hooks, hard links)"
dk run --rm --network "$N" $CLI sh -c "
apk add --no-cache git >/dev/null 2>&1
git config --global user.email t@test.invalid; git config --global user.name tester
git clone -q http://tester:Compat-Passw0rd%21@$G:3000/tester/demo.git /w && cd /w
echo one > a.txt && git add a.txt && git commit -q -m 'first' && git push -q origin main
head -c 3000000 /dev/urandom > big.bin && git add big.bin && git commit -q -m 'big' && git push -q origin main
git log --oneline | wc -l" > "$TMPD/git.out" 2>&1
ok "$(tail -1 "$TMPD/git.out" | grep -q '^3$'; echo $?)" "clone, commit and push of small and 3 MB files over HTTP (3 commits)"
ok "$(dk run --rm --network "$N" $CLI sh -c "apk add --no-cache git >/dev/null 2>&1; git clone -q http://tester:Compat-Passw0rd%21@$G:3000/tester/demo.git /w2 && cd /w2 && git fsck 2>&1 | grep -vi 'dangling' | wc -l | grep -q '^0$' && git log --oneline | wc -l | grep -q '^3$'"; echo $?)" "a fresh clone passes git fsck and has the history"
dk stop -t 30 "$G" >/dev/null
ok "$(c=$(exit_code "$G"); [ "$c" = 0 ] || [ "$c" = 143 ]; echo $?)" "graceful stop (exit $(exit_code "$G"))"
dk start "$G" >/dev/null
wait_for 180 sh -c "dk run --rm --network $N $CLI wget -q -O /dev/null -T 3 http://$G:3000/api/healthz"
ok "$(api http://$G:3000/api/v1/repos/tester/demo | grep -q '"name":"demo"'; echo $?)" "database and repository persist across a restart"
ok "$(dk run --rm --network "$N" $CLI sh -c "apk add --no-cache git >/dev/null 2>&1; git clone -q http://tester:Compat-Passw0rd%21@$G:3000/tester/demo.git /w3 && cd /w3 && git log --oneline | wc -l | grep -q '^3$'"; echo $?)" "the pushed commits are served after the restart"
logs_on_fail git
no_leftovers
