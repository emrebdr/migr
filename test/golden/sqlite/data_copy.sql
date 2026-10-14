-- migr:statement 1
INSERT INTO "users_archive" ("id", "email_address")
SELECT "users"."id", "users"."email" FROM "users";
