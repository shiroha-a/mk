DROP INDEX IF EXISTS "IDX_poll_expired_unnotified";
ALTER TABLE "poll" DROP COLUMN IF EXISTS "notifiedAt";
