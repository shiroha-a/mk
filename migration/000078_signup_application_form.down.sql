-- data loss: 申請フォームの定義と、各申請の回答が失われる。
ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "reason" varchar(2048);
ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "answers";
ALTER TABLE "meta" DROP COLUMN IF EXISTS "signupApplicationForm";
