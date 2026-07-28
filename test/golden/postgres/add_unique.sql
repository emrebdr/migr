-- migr:statement 1
ALTER TABLE "users" ADD CONSTRAINT "uq_users_email" UNIQUE ("email");
