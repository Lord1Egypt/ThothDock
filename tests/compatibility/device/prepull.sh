. /data/local/tmp/thoth-sysvipc/qa/env.sh
export COMPAT_DOCKER_HOST=unix://$Q/sock COMPAT_DOCKER_CLI=$L/libdocker.so DOCKER_CONFIG=$Q/cli HOME=$Q
sh /data/local/tmp/thoth-sysvipc/compat/runner/prepull.sh "$@"
