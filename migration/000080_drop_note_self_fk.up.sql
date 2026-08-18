-- #2623: note の自己参照 FK (renoteId / replyId) を落とす。
--
-- upstream Misskey の MiNote は reply / renote について
-- `createForeignKeyConstraints: false` を指定していて **FK を作らない**
-- (FK を持つのは user への `onDelete: 'CASCADE'` だけ)。mk-go は 000001 で
-- `ON DELETE SET NULL` の FK を張っていたため、元ノートが削除されると
-- リノート側の `renoteId` ごと NULL になっていた。
--
-- `renoteUserId` には FK が無いので値が残り、text も cw も添付も無い
-- 「本文も引用先も無いノート」がタイムラインに残る。frontend は
-- `renoteId` さえ残っていれば「削除されたノート」として描画できる
-- (`MkNote.vue` の `v-if="isRenote && note.renote == null"`) ので、
-- upstream に合わせて FK を外し、参照だけ残す。

ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_note_renoteId";
ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_note_replyId";

-- 既に SET NULL で壊れた行を削除する。
--
-- **この DELETE は復元できない。** `renoteId` が失われているため、どの
-- ノートへのリノートだったかは残っていない。表示のしようが無い
-- (本文も添付も引用先も無い) ため、残しても利用者には空欄が見えるだけになる。
--
-- 条件は「元が pure renote で、対象が消えたもの」に限定する。
-- `renoteUserId` が非 NULL なのに `renoteId` が NULL になるのは SET NULL の
-- 副作用でしか起こらない (通常の投稿経路は両方を同時に埋める)。本文・CW・
-- 返信・添付・投票のいずれかを持つ行は、リノート由来でも中身が残っている
-- ので削除しない。
--
-- drop-in (TS 製 DB へこの migration を流す場合): upstream は元から FK を
-- 持たないので SET NULL が起きず、この条件に合致する行は通常存在しない。
-- DROP CONSTRAINT も IF EXISTS なので無害に空振りする。制約差は mk-go 側に
-- だけあった余分な FK が消える方向にしか動かない。
DELETE FROM "note"
WHERE "renoteId" IS NULL
  AND "renoteUserId" IS NOT NULL
  AND (text IS NULL OR text = '')
  AND (cw IS NULL OR cw = '')
  AND "replyId" IS NULL
  AND ("fileIds" IS NULL OR "fileIds" = '{}')
  AND "hasPoll" = false;
