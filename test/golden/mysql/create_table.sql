-- migr:statement 1
CREATE TABLE `users` (
    `id` char(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    `email` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    `created_at` datetime(6) DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (`id`)
) DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
