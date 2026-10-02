-- The one privilege the lock-impact tests need and migr itself never does.
--
-- information_schema.INNODB_TABLES on MySQL and INNODB_SYS_TABLES on MariaDB both require PROCESS,
-- and the image's own grant covers one database. Those tables carry TABLE_ID, which InnoDB changes
-- when it rebuilds a table -- the direct analogue of the relfilenode probe the PostgreSQL driver's
-- suite uses, and the oracle behind "nothing rewrote that was not declared a rewrite" (M9-T3).
--
-- The driver reads neither table. A capability verdict is resolved with no I/O at all, so no migr
-- deployment needs this and nothing here is a hint about production grants.
GRANT PROCESS ON *.* TO 'migr'@'%';
FLUSH PRIVILEGES;
