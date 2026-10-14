-- migr:statement 1
INSERT INTO `users_archive` (`id`, `email_address`)
SELECT `id`, `email` FROM `users`;
