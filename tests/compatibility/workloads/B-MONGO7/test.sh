# B-MONGO7: MongoDB 7.0, the diagnostic control for B-MONGO8 (older TCMalloc).
# timeout: 420
. ../B-MONGO8/mongo.sh
mongo_db mongo:7.0
no_leftovers
