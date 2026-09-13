-- data loss: `fileHash` に入れた**スナップショットは復元できない**。drive の
-- ファイルが残っているものは up で再度 backfill されるが、申請者が消した分は
-- 戻らない (#2960)。
DROP INDEX IF EXISTS "IDX_emoji_application_name";
DROP INDEX IF EXISTS "IDX_emoji_application_remote_source";
DROP INDEX IF EXISTS "IDX_emoji_application_file_hash";
ALTER TABLE "emoji_application" DROP COLUMN IF EXISTS "fileHash";
