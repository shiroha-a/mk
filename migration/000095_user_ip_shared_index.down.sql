-- #3105 の巻き戻し。index の張り替えを元に戻すだけで、行は触らない。
CREATE INDEX IF NOT EXISTS "IDX_user_ip_ip_lastSeenAt" ON "user_ip" ("ip", "lastSeenAt" DESC);
DROP INDEX IF EXISTS "IDX_user_ip_ip_lastSeenAt_userId";
