-- #3103 の巻き戻し。
--
-- data loss: 戻らないものが 3 つある。
--
--   - 最終観測 (`lastSeenAt`) と観測回数 (`observationCount`) は列ごと消える
--   - up で正規化した `ip` の表記は元に戻らない (`::ffff:192.0.2.1` は
--     `192.0.2.1` のまま)
--   - 正規化で衝突して統合した行は復元できない (統合前の行数・個別の観測時刻は
--     残っていない)
--
-- `createdAt` の意味 (初回観測) も戻さない。列の中身は書き換えていないので、
-- 純正へ戻したときの読み方は up の前後で変わらない。
DROP INDEX IF EXISTS "IDX_user_ip_ip_lastSeenAt";
ALTER TABLE "user_ip" DROP COLUMN IF EXISTS "observationCount";
ALTER TABLE "user_ip" DROP COLUMN IF EXISTS "lastSeenAt";
