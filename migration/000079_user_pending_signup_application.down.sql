-- data loss: 確認待ちの承認済み登録と申請の紐付けが失われる。確認が完了しても
-- 申請が approved のまま残り、同じクレームコードで再度登録できる状態になる。
DROP INDEX IF EXISTS "IDX_user_pending_signupApplicationId";
ALTER TABLE "user_pending" DROP COLUMN IF EXISTS "signupApplicationId";
