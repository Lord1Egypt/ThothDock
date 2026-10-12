# A-MARIADB: MariaDB 11.8 LTS (InnoDB, io_uring or AIO, many threads).
# timeout: 600
. ../A-MYSQL/sql.sh
mysql_family mariadb:11.8 mariadb mariadb-admin
no_leftovers
