-- data loss: クレームコードで作られた申請はすべて失われる。連絡先の列を戻しても
-- 埋める値が無いため、行ごと削除する。
DELETE FROM "signup_application";

DROP INDEX IF EXISTS "IDX_signup_application_claimCodeHash";
ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "claimCodeHash";

ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "contactHost" varchar(128) NOT NULL DEFAULT '';
ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "contactRemoteId" varchar(32) NOT NULL DEFAULT '';
ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "contactUsername" varchar(128) NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS "IDX_signup_application_live_contact"
    ON "signup_application" ("contactHost", "contactRemoteId")
    WHERE "status" IN ('pending', 'approved');
