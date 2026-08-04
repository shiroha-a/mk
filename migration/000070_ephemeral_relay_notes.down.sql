-- 設定列のみの削除。Redis 上の ephemeral エントリは TTL で自然に消えるため、
-- ロールバックに伴う後始末は不要 (data loss も無い)。
ALTER TABLE "meta" DROP COLUMN IF EXISTS "ephemeralRelayNoteTtlMinutes";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "enableEphemeralRelayNotes";
