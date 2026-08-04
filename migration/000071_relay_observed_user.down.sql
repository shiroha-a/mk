-- 記録を落とすだけ。data loss は無い (再びリレー経由で観測すれば付き直す)。
-- user 行そのものには影響しない。
DROP TABLE IF EXISTS "relay_observed_user";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "relayOrphanUserGraceDays";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "enableRelayOrphanUserCleanup";
