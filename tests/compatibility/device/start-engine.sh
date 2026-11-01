# run under: run-as com.thothterm.debian.qa.sysvipc sh /data/local/tmp/thoth-sysvipc/qa/start-engine.sh
. /data/local/tmp/thoth-sysvipc/qa/env.sh
mkdir -p $F/linux/runtime $Q/root $Q/cli
cp /data/local/tmp/thoth-sysvipc/qa/libtalloc.so.2 /data/local/tmp/thoth-sysvipc/qa/libandroid-shmem.so $F/linux/runtime/
cp /data/local/tmp/thoth-sysvipc/qa/resolv.conf $Q/resolv.conf
for p in $(ps -A -o PID,ARGS | grep "[l]ibthothdock.so serve --root $Q" | awk '{print $1}'); do kill $p; done
sleep 2
rm -f $Q/sock
cd $Q
(setsid nohup $L/libthothdock.so serve --root $Q/root --socket $Q/sock --proot $L/libproot.so --proot-loader $L/libproot_loader.so --proot-lib-dir $F/linux/runtime --resolv-conf $Q/resolv.conf --shutdown-timeout 10 > $Q/daemon.log 2>&1 < /dev/null &)
sleep 3
dk version --format "{{.Server.Version}} {{.Server.APIVersion}}"
head -c 300 $Q/daemon.log
