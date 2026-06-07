-- #1527 review #2: note.uri に UNIQUE 制約を追加する。
--
-- 本家 Misskey の MiNote.uri は `@Index({ unique: true })` (varchar(512), nullable)
-- で、同一リモート note (uri = AP id) が二重取り込みされないことを DB で保証する。
-- mk-go はこの制約が無く、IngestNote の FindByURI→Create の間に同 URI の Create が
-- 並行すると重複行が作られ、引用元の renoteCount が二重増分される等の不整合が起きる
-- (federation 全般の dedup race)。
--
-- uri はローカル note では NULL (Misskey 同様「local の note は uri=null」)。実 query は
-- WHERE uri = $1 のみで NULL を引かないため、部分 UNIQUE index (WHERE uri IS NOT NULL)
-- にしてローカル note を制約対象から外し index を小さく保つ (000040 の drive_file と同方針)。
--
-- 既存の重複 uri 行があると UNIQUE index 作成に失敗するため、先に最小 id を残して
-- 重複を削除する。重複行は race で作られた冗長コピーなので削除は安全だが、当該重複
-- note を参照する子行 (reaction 等) は FK cascade で消える (data loss)。残す側 (最小 id)
-- の子行は影響を受けない。TS 移行済み DB / 新規 DB では重複は無く DELETE は no-op。
DELETE FROM "note" a
USING "note" b
WHERE a."uri" IS NOT NULL
  AND a."uri" = b."uri"
  AND a."id" > b."id";

-- 注: 大きな note テーブルでは UNIQUE index 構築中に書き込みが一時的にブロックされる。
-- 部分述語 (uri IS NOT NULL = remote note のみ) で対象を絞り構築時間を抑える。
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_note_uri" ON "note" ("uri") WHERE "uri" IS NOT NULL;
