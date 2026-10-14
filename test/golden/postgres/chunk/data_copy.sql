-- migr:chunk key (id)
-- migr:statement 1
SELECT "id"::text FROM "users" WHERE "id" > $1 ORDER BY "users"."id" LIMIT 1 OFFSET $2;

-- migr:statement 2
INSERT INTO "users_archive" ("id", "email_address")
SELECT "id", "email" FROM "users" WHERE "id" > $1 AND "id" <= $2;

-- migr:chunk key (tenant, id)
-- migr:statement 1
SELECT "tenant"::text, "id"::text FROM "users" WHERE ("tenant", "id") > ($1, $2) ORDER BY "users"."tenant", "users"."id" LIMIT 1 OFFSET $3;

-- migr:statement 2
INSERT INTO "users_archive" ("id", "email_address")
SELECT "id", "email" FROM "users" WHERE ("tenant", "id") > ($1, $2) AND ("tenant", "id") <= ($3, $4);
