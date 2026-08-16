-- data loss: 承認制の有効/無効の設定が失われる (列ごと消えるので既定の false 相当に戻る)。
-- signup_application の行は残るが、承認制が無効になるため参照されなくなる。
ALTER TABLE "meta" DROP COLUMN IF EXISTS "approvalRequiredForSignup";
