-- migr:chunk key (id)
-- migr:statement 1
SELECT `id` FROM `users` WHERE `id` > ? ORDER BY `id` LIMIT 1 OFFSET ?;

-- migr:statement 2
INSERT INTO `users_archive` (`id`, `email_address`)
SELECT `id`, `email` FROM `users` WHERE `id` > ? AND `id` <= ?;

-- migr:chunk key (tenant, id)
-- migr:statement 1
SELECT `tenant`, `id` FROM `users` WHERE `tenant` >= ? AND (`tenant` > ? OR (`id` > ?)) ORDER BY `tenant`, `id` LIMIT 1 OFFSET ?;

-- migr:statement 2
INSERT INTO `users_archive` (`id`, `email_address`)
SELECT `id`, `email` FROM `users` WHERE `tenant` >= ? AND (`tenant` > ? OR (`id` > ?)) AND `tenant` <= ? AND (`tenant` < ? OR (`id` <= ?));
