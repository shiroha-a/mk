-- data loss: 審査中・承認済みの申請がすべて失われる。
--
-- 承認済みだが未登録の申請を消すと、**その利用者は登録できなくなる**。発行済みの
-- registration_ticket は残るが、コードは利用者に渡していないため辿り着けない。
-- 申請し直してもらう以外の回復手段は無い。
--
-- 承認制を無効にしてから流すこと。
DROP INDEX IF EXISTS "IDX_signup_application_status_createdAt";
DROP INDEX IF EXISTS "IDX_signup_application_live_contact";
DROP TABLE IF EXISTS "signup_application";
