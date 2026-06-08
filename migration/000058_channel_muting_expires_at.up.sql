-- Add expiresAt to channel_muting for timed channel mutes (#1603).
-- Mirrors upstream Misskey channel_muting.expiresAt (nullable = indefinite).
ALTER TABLE "channel_muting" ADD COLUMN IF NOT EXISTS "expiresAt" timestamp with time zone;

-- Index on expiresAt to keep the read filters (expiresAt IS NULL OR expiresAt > now)
-- and the checkExpiredMutings prune (expiresAt < now) cheap. Same index name as
-- upstream (IDX_6dd314e96806b7df65ddadff72) for parity.
CREATE INDEX IF NOT EXISTS "IDX_6dd314e96806b7df65ddadff72" ON "channel_muting" ("expiresAt");
