-- migr:statement 1
ALTER TABLE "users" ADD COLUMN "nickname" varchar(64) DEFAULT 'anonymous';
