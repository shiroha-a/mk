-- Rollback initial schema
--
-- **`schema_migrations` は消さない。** golang-migrate が自分で管理するテーブルで
-- migration の対象ではない。ここで DROP すると `Down()` が最後に TRUNCATE しようと
-- して `relation "schema_migrations" does not exist (SQLSTATE 42P01)` で落ちる —
-- つまり `go run ./cmd/migrate -direction down` (全段ロールバック) が**毎回**
-- 最後に失敗していた。残しておいても中身は空になるので、次の up は version nil から
-- 始まる。
DROP TABLE IF EXISTS "meta" CASCADE;
DROP TABLE IF EXISTS "instance" CASCADE;
DROP TABLE IF EXISTS "emoji" CASCADE;
DROP TABLE IF EXISTS "access_token" CASCADE;
DROP TABLE IF EXISTS "following" CASCADE;
DROP TABLE IF EXISTS "note_reaction" CASCADE;
DROP TABLE IF EXISTS "poll" CASCADE;
DROP TABLE IF EXISTS "note" CASCADE;
DROP TABLE IF EXISTS "user_keypair" CASCADE;
DROP TABLE IF EXISTS "user_profile" CASCADE;
DROP TABLE IF EXISTS "drive_file" CASCADE;
DROP TABLE IF EXISTS "drive_folder" CASCADE;
DROP TABLE IF EXISTS "user" CASCADE;

DROP TYPE IF EXISTS followers_visibility_enum;
DROP TYPE IF EXISTS following_visibility_enum;
DROP TYPE IF EXISTS note_visibility_enum;
