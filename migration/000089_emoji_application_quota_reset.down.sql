-- data loss: **リセットの操作履歴** (誰がいつ何を理由に戻したか) は復元できない。
-- 申請そのものは `emoji_application` に残っているので、down 後は
-- リセットが無かったものとして数え直される = **枠が再び埋まる** (#2962)。
DROP INDEX IF EXISTS "IDX_emoji_application_quota_reset_user";
DROP TABLE IF EXISTS "emoji_application_quota_reset";
