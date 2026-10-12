# A-MYSQL: MySQL 8.4 LTS (InnoDB, native AIO, many threads).
# timeout: 600
. ../A-MYSQL/sql.sh
mysql_family mysql:8.4 mysql mysqladmin
no_leftovers
