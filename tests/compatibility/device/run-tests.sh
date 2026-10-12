# Run compatibility workloads on the phone, as the isolated QA package only:
#   run-as com.thothterm.debian.qa.sysvipc sh /data/local/tmp/thoth-sysvipc/qa/run-tests.sh LABEL ID...
# The app's SELinux domain cannot execute or access(X_OK) files in
# /data/local/tmp, so the probes are copied into the app's own directory.
. /data/local/tmp/thoth-sysvipc/qa/env.sh
label=$1; shift
mkdir -p $F/probes && cp /data/local/tmp/thoth-sysvipc/compat/probes/bin/* $F/probes/ && chmod 700 $F/probes/*
export COMPAT_DOCKER_HOST=unix://$Q/sock COMPAT_DOCKER_CLI=$L/libdocker.so DOCKER_CONFIG=$Q/cli HOME=$Q
export COMPAT_EVIDENCE=$Q/ev/$label COMPAT_ENGINE_ROOT=$Q/root COMPAT_PROBES=$F/probes
export TMPDIR=$Q/tmp; mkdir -p $TMPDIR
cd /data/local/tmp/thoth-sysvipc/compat
sh runner/run.sh "$@"
