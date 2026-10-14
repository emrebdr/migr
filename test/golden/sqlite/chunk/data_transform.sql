-- migr:chunk key (id)
-- migr:statement 1
SELECT "users"."id" FROM "users" WHERE "users"."id" > ? ORDER BY "users"."id" LIMIT 1 OFFSET ?;

-- migr:statement 2
UPDATE "users" SET "email_key" = lower("users"."email") WHERE "users"."id" > ? AND "users"."id" <= ?;

-- migr:chunk key (tenant, id)
-- migr:statement 1
SELECT "users"."tenant", "users"."id" FROM "users" WHERE ("users"."tenant", "users"."id") > (?, ?) ORDER BY "users"."tenant", "users"."id" LIMIT 1 OFFSET ?;

-- migr:statement 2
UPDATE "users" SET "email_key" = lower("users"."email") WHERE ("users"."tenant", "users"."id") > (?, ?) AND ("users"."tenant", "users"."id") <= (?, ?);
