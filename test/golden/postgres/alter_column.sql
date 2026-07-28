-- migr:statement 1
ALTER TABLE "users" ALTER COLUMN "email" TYPE varchar(512);

-- migr:statement 2
ALTER TABLE "users" ALTER COLUMN "email" DROP NOT NULL;
