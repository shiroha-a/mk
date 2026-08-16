-- 承認済み登録をメール確認の経路に乗せる (#2568 / #2571)。
--
-- メール必須のとき、承認済みからの登録も user_pending に積んで確認メールを
-- 送る。この時点ではまだアカウントが無いので、申請を completed にできない。
--
-- **紐付けが無いと 1 つの承認から複数アカウントを作れる。** 申請が approved の
-- まま残るため、申請者は別の username で何度でも登録を始められてしまう。pending
-- 側に申請 ID を持たせ、確認完了時に申請を completed にする。
--
-- invitationTicketId と同じ形。TS は未知の列を無視するので drop-in の復路は
-- 壊れない。
ALTER TABLE "user_pending" ADD COLUMN IF NOT EXISTS "signupApplicationId" varchar(32);

-- 承認済み登録の確認待ちを申請から引くための索引。件数は少ないが、確認完了時に
-- 必ず 1 回引く。
CREATE INDEX IF NOT EXISTS "IDX_user_pending_signupApplicationId"
    ON "user_pending" ("signupApplicationId");
