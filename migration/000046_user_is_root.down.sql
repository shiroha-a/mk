-- 000046 の rollback。drop-in 経路で TS が同名 column を持っていた場合に
-- DROP すると TS への切り戻しで isRoot 値が失われるため、運用上 down は
-- 慎重に。DROP IF EXISTS は実行するが、TS に切り戻す際は TS の migration
-- が同 column を再作成する (ただし isRoot=true は復元されない)。
ALTER TABLE "user" DROP COLUMN IF EXISTS "isRoot";
