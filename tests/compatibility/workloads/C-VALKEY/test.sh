# C-VALKEY: Valkey 8 (Redis-protocol fork), same checks as C-REDIS.
# timeout: 240
. ../C-REDIS/kv.sh
kv_store valkey/valkey:8-alpine valkey-server valkey-cli valkey-benchmark
no_leftovers
