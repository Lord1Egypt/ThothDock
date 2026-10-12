# H-SEAWEEDFS: SeaweedFS (Go; master, volume, filer and S3 gateway in one
# process): bucket, object upload/download with SHA-256 check, restart
# persistence. The S3 gateway runs without credentials, on the test network
# only. Explicit alternative to H-MINIO, not a substitute result for it.
# timeout: 300
IMG=chrislusf/seaweedfs:latest
dk pull -q $IMG >/dev/null; dk pull -q nginx:1.28-alpine >/dev/null
setup
V=$(name data); dk volume create "$V" >/dev/null
# No network namespace: left alone, SeaweedFS advertises and binds a host
# interface address (the LAN address on a phone) and port 8080. -ip names
# the container (its own 127.77.x.y address on the test network).
S=$(name s3)
run_bg s3 -v "$V":/data $IMG server -dir=/data -ip="$S" -ip.bind=0.0.0.0 -volume.port=28080 \
    -s3 -s3.port=8333 -master.volumeSizeLimitMB=64 -volume.max=4
c() { dk run --rm --network "$N" nginx:1.28-alpine "$@"; }
wait_for 120 c curl -sf -X PUT "http://$S:8333/compat-bucket"
ok $? "S3 gateway up, bucket created"
sum=$(c sh -c "head -c 3000000 /dev/urandom > /tmp/o && sha256sum /tmp/o | cut -d' ' -f1 && curl -sf -X PUT --data-binary @/tmp/o http://$S:8333/compat-bucket/object.bin >/dev/null && curl -sf http://$S:8333/compat-bucket/object.bin | sha256sum | cut -d' ' -f1")
ok "$([ "$(echo "$sum" | sed -n 1p)" = "$(echo "$sum" | sed -n 2p)" ] && [ -n "$(echo "$sum" | sed -n 2p)" ]; echo $?)" "3 MB object round trip, SHA-256 equal"
before=$(echo "$sum" | sed -n 1p)
t0=$(date +%s); dk stop -t 60 "$S" >/dev/null  # its graceful shutdown takes ~25 s
note "stop took $(( $(date +%s) - t0 )) s"
ok "$(c2=$(exit_code "$S"); [ "$c2" = 0 ] || [ "$c2" = 143 ]; echo $?)" "docker stop ends the server (exit $(exit_code "$S"))"
dk start "$S" >/dev/null
wait_for 120 c curl -sf -o /dev/null "http://$S:8333/compat-bucket/object.bin"
after=$(c sh -c "curl -sf http://$S:8333/compat-bucket/object.bin | sha256sum | cut -d' ' -f1")
ok "$([ "$after" = "$before" ]; echo $?)" "object intact after restart"
logs_on_fail s3
no_leftovers
