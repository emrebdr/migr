-- migr:statement 1
UPDATE "users" SET "email_key" = lower("users"."email");
