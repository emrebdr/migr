-- migr:statement 1
CREATE VIEW "active_users" AS SELECT id FROM users WHERE active;
