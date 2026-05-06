-- 000046 の rollback。drop-in 経路で TS が同名 column を持っていた場合に
-- DROP すると TS への切り戻しでデータ損失するため、本番 DB では down は実質
-- no-op に近い (DROP IF EXISTS は実行するが TS が再起動時に再作成する)。
ALTER TABLE "user" DROP COLUMN IF EXISTS "isRoot";
