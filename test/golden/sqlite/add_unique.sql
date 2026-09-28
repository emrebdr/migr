-- migr:statement 1
CREATE UNIQUE INDEX "uq_users_email" ON "users" ("email");
