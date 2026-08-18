-- #2623 の巻き戻し。000001 と同じ定義で自己参照 FK を張り直す。
--
-- **削除した孤児行は戻らない** (up 側のコメント参照)。ここで復元するのは
-- 制約だけ。
--
-- FK を外している間に「対象が削除されたリノート / 返信」が生まれるので、
-- 先に参照先の無い値を NULL に落とさないと `ADD CONSTRAINT` が制約違反で
-- 失敗する。この UPDATE は up 側が防ごうとしていた状態 (renoteId の消失) を
-- 意図的に作ることになるが、FK を復元する以上は避けられない。
UPDATE "note" SET "renoteId" = NULL
WHERE "renoteId" IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM "note" t WHERE t."id" = "note"."renoteId");

UPDATE "note" SET "replyId" = NULL
WHERE "replyId" IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM "note" t WHERE t."id" = "note"."replyId");

ALTER TABLE "note" ADD CONSTRAINT "FK_note_replyId"
  FOREIGN KEY ("replyId") REFERENCES "note"("id") ON DELETE SET NULL;
ALTER TABLE "note" ADD CONSTRAINT "FK_note_renoteId"
  FOREIGN KEY ("renoteId") REFERENCES "note"("id") ON DELETE SET NULL;
