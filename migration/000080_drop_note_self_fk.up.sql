-- #2623: note の自己参照 FK (renoteId / replyId) を落とす。
--
-- upstream Misskey は 2025-07-30 の migration 1753868431598
-- (RemoveNoteConstraints) でこの 2 本の FK を**削除した**。現在の `MiNote` は
-- `createForeignKeyConstraints: false` を指定していて FK を作らない。
-- mk-go は 000001 で `ON DELETE SET NULL` の FK を張ったままだった。
--
-- そのため元ノートが削除されるとリノート側の `renoteId` ごと NULL になる。
-- `renoteUserId` には FK が無いので値が残り、text も cw も添付も無い
-- 「本文も引用先も無いノート」がタイムラインに残る。frontend は `renoteId`
-- さえ残っていれば「削除されたノート」として描画できる
-- (`MkNote.vue` の `v-if="isRenote && note.renote == null"`) ので、upstream に
-- 合わせて FK を外し参照だけ残す。
--
-- **upstream 由来の名前も落とす。** upstream が FK を持っていた頃の名前は
-- `FK_17cb3553c700a4985dff5a30ff5` (replyId) / `FK_52ccc804d7c69037d558bac4c96`
-- (renoteId) で、その定義は **ON DELETE CASCADE**。1753868431598 を適用して
-- いない TS 製 DB を引き継ぐと、mk-go 名を落としても CASCADE 側が残り、
-- 「元ノートを消すとリノートごと消える」という SET NULL より重い壊れ方をする。
-- 名前が違うだけで対象は同じ列なので、両方落として初めて意図した状態になる。
--
-- 孤児行の後始末は 000081 で行う。**DDL と大量 DELETE を同じファイルに置くと、
-- DROP CONSTRAINT が取る ACCESS EXCLUSIVE を DELETE の全表スキャンの間ずっと
-- 保持する** (golang-migrate はファイルを 1 つの Exec で送るため、暗黙の
-- 単一トランザクションになる)。note は最大テーブルなので分ける。

ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_note_renoteId";
ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_note_replyId";

-- upstream (TypeORM) が生成した hash 名。TS 製 DB を引き継いだ場合にだけ存在する。
ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_52ccc804d7c69037d558bac4c96";
ALTER TABLE "note" DROP CONSTRAINT IF EXISTS "FK_17cb3553c700a4985dff5a30ff5";
