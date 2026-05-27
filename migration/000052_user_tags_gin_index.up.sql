-- hashtags/users は user.tags 配列の containment (tags @> ARRAY[...]) で
-- 指定 hashtag を持つユーザーを引く。GIN インデックスで containment を
-- 高速化する (note.tags の IDX_note_tags と同方針, #655)。
-- upstream User entity の user.tags index は btree (配列 containment には
-- 効かない) なので、mk-go は GIN を採用して query を実効化する。
CREATE INDEX IF NOT EXISTS "IDX_user_tags" ON "user" USING gin ("tags");
