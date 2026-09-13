-- カスタム絵文字申請に、申請時点の画像ハッシュをスナップショットとして持つ (#2960)。
--
-- **審査の材料としてのみ使う。** 同じ画像が過去に却下されていたことを
-- モデレーターに見せるためのもので、自動拒否や重複判定には使わない
-- (ライセンスの変更・画像の修正・運用方針の変更がありうる)。
--
-- **drive を引き直さずスナップショットで持つ。** 申請者は審査を待つ間に
-- drive のファイルを消せるので、照合のたびに引くと「消した申請は履歴から
-- 消える」ことになり、過去の判断を追えなくなる。
ALTER TABLE "emoji_application" ADD COLUMN IF NOT EXISTS "fileHash" varchar(32);

-- 既存分は **drive にファイルが残っているものだけ** 埋める。消えているものは
-- NULL のままにして、名前とリモート元による照合だけを効かせる。
UPDATE "emoji_application" a
SET "fileHash" = f."md5"
FROM "drive_file" f
WHERE a."fileId" = f."id" AND a."fileHash" IS NULL AND f."md5" <> '';

-- 照合用の索引。**部分索引にする** — own の申請にしか値が入らないので、
-- NULL を含めると行の大半を占める remote 側まで索引に載る。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_file_hash"
    ON "emoji_application" ("fileHash") WHERE "fileHash" IS NOT NULL;

-- リモート元での照合。こちらも remote の申請にしか値が入らない。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_remote_source"
    ON "emoji_application" ("remoteHost", "remoteName") WHERE "remoteHost" IS NOT NULL;

-- 名前での照合。既存の "IDX_emoji_application_pending_name" は
-- (userId, name) WHERE status = 'pending' なので、**他人の・処理済みの**
-- 申請を名前で引くのには使えない。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_name"
    ON "emoji_application" ("name");
