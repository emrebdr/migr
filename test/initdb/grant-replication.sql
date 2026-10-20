-- The two privileges a binary-log reader needs, which a shadow-table migration on MySQL reads through
-- (M13-T10).
--
-- REPLICATION SLAVE lets a session stream the binary log, and REPLICATION CLIENT lets it ask where the
-- log ends. migr_plain is left without either, so a server that permits capture still refuses that user
-- by name -- the capture fact is read from the server, and a fact only ever read as one answer has not
-- been tested. MariaDB runs this file too and stays refused for log_bin.
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'migr'@'%';
FLUSH PRIVILEGES;
