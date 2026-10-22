-- Every privilege a binlog reader needs on MariaDB, for mariadb114binlog alone (M13-T17).
--
-- REPLICATION SLAVE streams the log and BINLOG MONITOR (REPLICATION CLIENT's name from 10.5) reads where
-- it ends, as initdb/grant-replication.sql grants on the other servers. From 10.5.2 SHOW SLAVE HOSTS asks
-- for REPLICATION MASTER ADMIN as well, and a reader lists every replica's server id through it so it
-- never registers under one: MariaDB disconnects one of two readers sharing an id. MySQL has no
-- privilege of that name, which is why this server has a directory of its own rather than sharing
-- initdb. PROCESS is initdb/grant-process.sql's, for the same lock-impact probes.
GRANT REPLICATION SLAVE, BINLOG MONITOR, REPLICATION MASTER ADMIN, PROCESS ON *.* TO 'migr'@'%';
FLUSH PRIVILEGES;
