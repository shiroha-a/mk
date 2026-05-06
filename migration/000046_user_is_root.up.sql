-- user.isRoot column を追加 (#785)。
--
-- Misskey TS upstream は signup された最初のユーザー (= instance owner) を
-- user.isRoot=true でマークする。一方 mk-go は meta.rootUserId で root 判別
-- していたため、TS DB を引き継いだ drop-in 環境では root が認識されず admin
-- path で 403 (ROLE_PERMISSION_DENIED) になる regression があった。
--
-- 本 migration で mk-go pure に build した DB にも isRoot column を持たせ、
-- role.Service.isRootUser が user.isRoot を fallback として読めるようにする。
-- 既に TS 由来の DB (drop-in 経路で TS が schema を作ったケース) には column
-- が存在しているので IF NOT EXISTS で idempotent に。
ALTER TABLE "user"
    ADD COLUMN IF NOT EXISTS "isRoot" boolean NOT NULL DEFAULT false;
