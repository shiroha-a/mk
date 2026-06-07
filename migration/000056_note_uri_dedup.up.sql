-- #1527 review #2 (前半): note.uri の重複行を除去する。
--
-- mk-go は note.uri に UNIQUE 制約が無く、IngestNote の FindByURI→Create の間に
-- 同一 URI の Create が並行すると重複行が作られていた (federation の dedup race)。
-- 000057 で UNIQUE index を張る前に、既存の重複 uri 行を最小 id を残して除去する
-- (重複が残ると CONCURRENTLY build が INVALID になるため)。
--
-- 重複行は race で作られた冗長コピーなので削除して問題ない。FK の挙動:
--   - note_reaction / poll 等 (noteId が ON DELETE CASCADE) → 当該重複行ぶんは削除される
--   - 他 note の renoteId / replyId (ON DELETE SET NULL) → 参照していた note 自体は
--     残り、リンクのみ NULL 化される (= note 本体の data loss ではない)
-- 残す側 (最小 id) の子行・被参照は影響を受けない。TS 移行済み / 新規 DB では重複は
-- 無いので DELETE は事実上 no-op。
--
-- 注: uri は未 index なので self-join は remote note 部分集合の scan になる。重複が
-- 無ければ削除 0 行で、長時間の排他ロックは取らない (DELETE は対象行のみ行ロック)。
DELETE FROM "note" a
USING "note" b
WHERE a."uri" IS NOT NULL
  AND a."uri" = b."uri"
  AND a."id" > b."id";
