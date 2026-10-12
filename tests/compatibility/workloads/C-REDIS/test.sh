# C-REDIS: Redis 8 (event loop, AOF fsync, TTL, concurrent clients).
# timeout: 240
. ../C-REDIS/kv.sh
kv_store redis:8-alpine redis-server redis-cli redis-benchmark
no_leftovers
