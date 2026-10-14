-- migr:chunk key (id)
-- migr:statement 1
SELECT "users"."id" FROM "users" WHERE "users"."id" > ? ORDER BY "users"."id" LIMIT 1 OFFSET ?;

-- migr:statement 2
INSERT INTO "users_archive" ("id", "email_address")
SELECT "users"."id", "users"."email" FROM "users" WHERE "users"."id" > ? AND "users"."id" <= ?;

-- migr:chunk key (tenant, id)
-- migr:statement 1
SELECT "users"."tenant", "users"."id" FROM "users" WHERE ("users"."tenant", "users"."id") > (?, ?) ORDER BY "users"."tenant", "users"."id" LIMIT 1 OFFSET ?;

-- migr:statement 2
INSERT INTO "users_archive" ("id", "email_address")
SELECT "users"."id", "users"."email" FROM "users" WHERE ("users"."tenant", "users"."id") > (?, ?) AND ("users"."tenant", "users"."id") <= (?, ?);
