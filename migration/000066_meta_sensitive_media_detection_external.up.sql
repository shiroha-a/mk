-- upstream 2026.7.0 (#17570): センシティブメディア判定を外部サービス
-- (misskey-dev/sensitive-detector) への HTTP 呼び出しに移行するための接続
-- 設定。upstream migration 1780488454126-sensitiveMediaDetectionExternalService
-- と同一 DDL。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "sensitiveMediaDetectionApiUrl" character varying(1024);
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "sensitiveMediaDetectionApiKey" character varying(1024);
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "sensitiveMediaDetectionTimeout" integer NOT NULL DEFAULT '60000';
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "sensitiveMediaDetectionMaxImagesPerRequest" integer NOT NULL DEFAULT '4';
