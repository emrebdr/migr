-- migr:statement 1
ALTER TABLE `users` ADD COLUMN `nickname` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT 'anonymous';
