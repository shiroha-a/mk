-- #3105: 関連アカウント候補の抽出を index だけで打ち切れるようにする。
--
-- #3103 が張った `(ip, "lastSeenAt" DESC)` は「その IP を使ったアカウントを
-- 最終観測の新しい順に」までは効くが、**同じ最終観測が並んだときの順序を
-- 決められない**。関連候補の抽出は 1 つの IP から取る件数を上限で打ち切るので、
-- 順序が決まらないと**同じ検索が実行のたびに違う候補を返す**。
--
-- 順序を `("lastSeenAt" DESC, "userId" ASC)` にすると、`userId` が index に無いぶん
-- PostgreSQL は `lastSeenAt` を presorted key とする incremental sort に倒れる。
-- **上限が保証されるかは `lastSeenAt` の同値の分布に依る** — 同じ値が固まっている
-- と、そのグループを全部読んでから並べ替えることになる。実測 (PG 15、1 IP に
-- 10 万アカウント、`LIMIT 201`):
--
--   lastSeenAt が行ごとに違う   旧 index 202 行 / 0.14 ms、新 index 201 行 / 0.11 ms
--   lastSeenAt が全行同じ       旧 index 100,000 行 / 33.7 ms、新 index 201 行 / 0.17 ms
--
-- 実態では `lastSeenAt` は観測のたびに更新される timestamptz なので同値は固まり
-- にくいが、**分布に依らず上限を保証したい**のでここで index に足す。
-- 記録が止まっている期間や、移行直後に一括で入った行では同値が固まりうる。
--
-- **足すのではなく張り替える。** 新しい index は古いものの列を先頭から含むので、
-- 古いほうが担っていた検索はそのまま乗る。両方残すと同じ用途の index が 2 つ
-- 並び、書き込みのたびに両方が更新される。
CREATE INDEX IF NOT EXISTS "IDX_user_ip_ip_lastSeenAt_userId"
    ON "user_ip" ("ip", "lastSeenAt" DESC, "userId");
DROP INDEX IF EXISTS "IDX_user_ip_ip_lastSeenAt";
