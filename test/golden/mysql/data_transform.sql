-- migr:statement 1
UPDATE `users` SET `email_key` = LOWER(CAST(`email` AS CHAR));
