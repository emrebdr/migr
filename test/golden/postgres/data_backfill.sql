-- migr:statement 1
UPDATE "users" SET "nickname" = 'anonymous' WHERE "nickname" IS NULL;
