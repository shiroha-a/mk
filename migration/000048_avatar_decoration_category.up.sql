-- upstream Misskey #17034 (= 2026.5.0 / triage follow-up): avatar_decoration に
-- category column を追加し、管理画面でカテゴリ分類できるようにする。nullable
-- なので既存行の影響なし。
--
-- IF NOT EXISTS 必須 (#2246): upstream も 2026.5.0 の
-- AddCategoryToAvatarDecorations1766652173085 で同じ列を追加しているため、
-- TS 製 DB に drop-in すると既に存在する。付けないと
-- `column "category" of relation "avatar_decoration" already exists` で
-- migration が dirty 停止し、drop-in 手順そのものが完走しない。
ALTER TABLE "avatar_decoration" ADD COLUMN IF NOT EXISTS "category" varchar(128);
