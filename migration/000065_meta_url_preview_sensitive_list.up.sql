-- upstream 2026.7.0 (#17635): 条件に一致した URL プレビューのサムネイルを
-- 隠せるようにするための keyword リスト。upstream migration
-- 1782581064131-urlPreviewSensitiveList と同一 DDL。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "urlPreviewSensitiveList" character varying(3072)[] NOT NULL DEFAULT '{}';
