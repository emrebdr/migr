#!/bin/sh
# Makes this server a replica of the compose mysql80, three seconds behind it (M13-T8).
#
# It starts from the source's position now rather than from its first binary log, so nothing the
# other suites did before this container came up reaches it; and --replicate-do-db keeps what they do
# afterwards out too. SOURCE_DELAY holds back applying and not receiving, which is the lag
# SHOW REPLICA STATUS reports as Seconds_Behind_Source for every write the tests make.
set -e

on_source() {
	mysql -h mysql80 -uroot -p"$MYSQL_ROOT_PASSWORD" -N -e "$1"
}

until on_source "SELECT 1" >/dev/null 2>&1; do sleep 1; done

position=$(on_source "SHOW MASTER STATUS")
file=$(echo "$position" | awk '{print $1}')
offset=$(echo "$position" | awk '{print $2}')

mysql -uroot -p"$MYSQL_ROOT_PASSWORD" <<SQL
GRANT REPLICATION CLIENT ON *.* TO 'migr'@'%';
FLUSH PRIVILEGES;
CHANGE REPLICATION SOURCE TO SOURCE_HOST='mysql80', SOURCE_PORT=3306, SOURCE_USER='root',
  SOURCE_PASSWORD='$MYSQL_ROOT_PASSWORD', GET_SOURCE_PUBLIC_KEY=1, SOURCE_DELAY=3,
  SOURCE_LOG_FILE='$file', SOURCE_LOG_POS=$offset;
START REPLICA;
SQL
