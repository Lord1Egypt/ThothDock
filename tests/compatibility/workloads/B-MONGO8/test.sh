# B-MONGO8: MongoDB 8.0.32 (bundled TCMalloc assumes a 48-bit address space).
# timeout: 420
. ../B-MONGO8/mongo.sh
mongo_db mongo:8.0.32
no_leftovers
