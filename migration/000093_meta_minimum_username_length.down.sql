-- data loss: 最小文字数の設定値が失われる (列ごと消えるので既定の 1 相当に戻る)。
-- 既に作られたアカウントには影響しない (登録時にしか見ない設定のため)。
ALTER TABLE "meta" DROP COLUMN IF EXISTS "minimumUsernameLength";
