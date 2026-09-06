-- #2877 のセキュリティ修正で `users/get-frequently-replied-users` の集計に
-- `ORDER BY "id" DESC LIMIT 1000` の窓を入れたが、**mk-go の schema では
-- この窓が成立しない**。
--
-- upstream は 2025-04 (`1745378064470-composite-note-index.js`) で `note` の
-- index を `("userId")` から `("userId", "id" DESC)` へ張り替えている。mk-go は
-- `000001_initial` の `IDX_note_userId` (単独) のまま追随していなかった。
--
-- 単独 index で `ORDER BY "id" DESC LIMIT 1000` を解こうとすると、planner は
-- 「一様分布なら 1000 件はすぐ見つかる」と見積もって
-- `Index Scan Backward using note_pkey` に倒れる。**最後の返信が古い利用者**
-- (成熟インスタンスでは大多数) では最後まで見つからず、note テーブルを
-- ほぼ全部読む。実測 (note 2.18M 行、休眠・他者宛 50k):
--
--   単独 index のみ : 400.0 ms / buffers 62,498 (約 488MB)
--   複合 index あり :   2.9 ms / buffers 数百
--
-- この endpoint は upstream / mk-go とも**未認証で任意の userId に対して**
-- 叩けてレート制限も無いので、古い休眠アカウントを 1 つ選ぶだけで最悪ケースを
-- 引ける。窓を入れた副作用で走査量の上界が「対象ユーザーの投稿数」から
-- 「そのユーザーの最終返信以降にインスタンス全体が積んだノート数」に変わった。
--
-- **index 名は upstream と同じにする。** TS 生まれの DB には upstream の
-- migration で同名の index が既にあるので、別名を使うと同内容の index が
-- 二重にできる (`known_duplicate_indexes.json` に載せる羽目になる)。
CREATE INDEX IF NOT EXISTS "IDX_724b311e6f883751f261ebe378" ON "note" ("userId", "id" DESC);

-- 複合 index が `("userId")` を prefix としてカバーするので単独 index は冗長。
-- upstream も同じ migration で落としている。
DROP INDEX IF EXISTS "IDX_note_userId";

-- planner に複合 index の統計を学習させる。upstream のコメント曰く
-- 「複雑なクエリでもこの index を先に使うと結果集合が大きく絞れる」ことを
-- 認識させるのに要る。
ANALYZE "user", "note";
