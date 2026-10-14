-- migr:statement 1
UPDATE "users" SET "nickname" = 'anonymous' WHERE "users"."nickname" IS NULL;
