-- data loss: 進行中の分割アップロードセッションは失われる。ロールバック前に
-- S3 側の未完了マルチパートアップロードが残っていないか確認すること
-- (バケットの incomplete multipart upload ライフサイクルルールでも回収できる)。
DROP TABLE IF EXISTS "chunked_upload_session";

ALTER TABLE "meta" DROP COLUMN IF EXISTS "chunkedUploadMaxPendingMbPerUser";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "chunkedUploadMaxSessionsPerUser";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "chunkedUploadSessionTtlMinutes";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "chunkedUploadChunkSizeMb";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "chunkedUploadEnabled";
