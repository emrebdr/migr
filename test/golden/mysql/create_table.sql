-- migr:statement 1
CREATE TABLE `users` (
    `id` char(36) NOT NULL,
    `email` varchar(255) NOT NULL,
    `created_at` datetime(6) DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (`id`)
);
