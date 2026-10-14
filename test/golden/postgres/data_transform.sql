-- migr:statement 1
UPDATE "users" SET "email_key" = CAST(lower(CAST("email" AS text)) AS varchar(255));
