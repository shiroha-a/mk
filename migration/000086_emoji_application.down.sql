-- data loss: 審査中・審査済みの申請がすべて消える。承認済みの申請から作られた
-- `emoji` 行は残るが、誰がいつ何を根拠に申請したかという経緯は復元できない。
DROP INDEX IF EXISTS "IDX_emoji_application_pending_name";
DROP INDEX IF EXISTS "IDX_emoji_application_user_created";
DROP INDEX IF EXISTS "IDX_emoji_application_status_created";
DROP TABLE IF EXISTS "emoji_application";
