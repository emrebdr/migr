-- migr:statement 1
CREATE TABLE "users" (
    "id" uuid NOT NULL,
    "email" varchar(255) NOT NULL,
    "created_at" timestamptz DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY ("id")
);
