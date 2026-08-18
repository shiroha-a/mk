-- #2623 の巻き戻し。000001 と同じ定義で mk-go 側の自己参照 FK を張り直す。
--
-- **NOT VALID を付けるのが要点。** FK を外している間に「対象が削除された
-- リノート / 返信」が正常に生まれる (それがこの migration の目的そのもの)。
-- 通常の ADD CONSTRAINT は既存行を全件検証するので、そのままでは 23503 で
-- 失敗して巻き戻せない。かといって参照先の無い値を NULL へ落とすと、
-- **000080 が直そうとした壊れ方を down 自身が作り出し**、再度 up したときに
-- 000081 の DELETE がそれらを消してしまう (down → up でデータが消える)。
--
-- NOT VALID なら既存行は検証せず、以後の INSERT / UPDATE にだけ制約が効く。
-- 既存行も検証したくなったら、参照先を整えたうえで
-- `ALTER TABLE "note" VALIDATE CONSTRAINT "FK_note_renoteId";` を別途実行する
-- (VALIDATE は ACCESS EXCLUSIVE を取らないので運用中でも流せる)。
--
-- upstream 由来の名前 (FK_52ccc80... / FK_17cb355...) は復元しない。あれは
-- TS 側の migration が管理するもので、mk-go が勝手に作り直すと TS へ戻した
-- ときに TypeORM の状態と食い違う。

ALTER TABLE "note" ADD CONSTRAINT "FK_note_replyId"
  FOREIGN KEY ("replyId") REFERENCES "note"("id") ON DELETE SET NULL NOT VALID;
ALTER TABLE "note" ADD CONSTRAINT "FK_note_renoteId"
  FOREIGN KEY ("renoteId") REFERENCES "note"("id") ON DELETE SET NULL NOT VALID;
