-- #2994: chat_room をホストごとに区別できるようにする。
--
-- `chat_room` は room id だけで keying しており、**どのホストの room かを持って
-- いない**。room id は相手が自由に決められる値 (`https://<host>/chat/rooms/{id}`
-- から取り出す) なので、ID 空間はホストをまたいで共有されている。
--
-- 実害は「先に作ったほうが勝つ」形で出る。あるホストが id `X` の room を先に
-- 作ると、別のホストの正規の room `X` からの Invite は `EnsureRoomViaAP` の
-- owner 不一致で**恒久的に drop される** (retry もされない)。
--
-- **PK は `id` のまま。** `(id, host)` の複合キーにはしない:
--   - `host` が NULL のままでは PK に入れられない (PostgreSQL の制約)
--   - UNIQUE へ逃がして複合 FK を張っても、`MATCH SIMPLE` は**FK 列のどれかが
--     NULL なら検査を素通りする**ので、ローカル room だけ整合性が消える
--   - upstream Misskey の `MiChatRoom` は `@PrimaryColumn() id` なので、PK を
--     変えると TS へ戻せなくなる (drop-in)
--
-- かわりに `id` を**ローカルの行 ID**として扱い、リモート room の身元は `uri`
-- (正規 URI) で持つ。`chat_message.uri` と同じ形で、列の追加だけなので drop-in を
-- 壊さない。リモート room は以後ローカルで採番した id を持つため、ローカルの
-- room id 空間を先取りされても衝突しない。
ALTER TABLE "chat_room" ADD COLUMN IF NOT EXISTS "host" varchar(128);
ALTER TABLE "chat_room" ADD COLUMN IF NOT EXISTS "uri" varchar(512);

-- 既存のリモート room を backfill する。
--
-- **今の行は `id` が origin の room id そのもの**なので、owner の actor から
-- `scheme://authority` を取れば正規 URI を復元できる。
--
-- **authority は `user.uri` からではなく `user.host` から組む。** 実行時の引き当ては
-- `idnhost.CanonicalURI` を通した正規形で行う (小文字化 + 既定ポート除去 +
-- punycode) のに対し、`user.uri` は相手が名乗った生の actor id で、
-- `https://Mixed.Example/...` や `:443` 付きや IDN のままでありうる。生のまま刻むと
-- **その room 宛の Accept / 本文が引き当てに失敗して恒久的に落ちる** (しかも配送側は
-- 古い綴りを名乗り続けるので誰も気付かない)。`user.host` は #2706 以降
-- `hostFromURI` が同じ正規化を掛けて保存しているので、そのまま使える。
-- SQL に IDNA が無い以上、ここは `user.host` を経由するしかない。
--
-- scheme だけは `user.uri` から取る (http で連合している相手が居る)。取れなければ
-- https に倒す — **埋め損ねると、次の Invite が同じ room を別の行として作り直し、
-- 既存の membership と invitation が宙に浮く。**
--
-- **`rooms/transfer-ownership` で owner をリモート利用者にした room は区別できない。**
-- その endpoint は #2994 でローカル利用者限定にしたが、それ以前に譲渡した行は
-- 「owner がリモート」だけを見るこの UPDATE に巻き込まれ、**こちらの room に
-- 存在しないリモート URI が刻まれる**。使った覚えがあるなら流す前に確認すること:
--   SELECT r.id, r."ownerId" FROM "chat_room" r JOIN "user" u ON u.id = r."ownerId"
--    WHERE u.host IS NOT NULL;
-- こちらが作った room が混じっていれば、流した後に
--   UPDATE "chat_room" SET host = NULL, uri = NULL WHERE id IN (...);
-- で戻す (ローカルの room は host / uri とも NULL が正しい形)。
UPDATE "chat_room" r
SET "host" = u."host",
    "uri"  = COALESCE(substring(u."uri" from '^(https?)://'), 'https')
             || '://' || u."host" || '/chat/rooms/' || r."id"
FROM "user" u
WHERE u."id" = r."ownerId"
  AND u."host" IS NOT NULL
  AND r."uri" IS NULL;

-- ローカル room は `host` / `uri` とも NULL のまま (`note` / `emoji` と同じ規約)。
-- 部分 index にしてローカル分を除外する。
--
-- **UNIQUE なのは同一 room の二重取り込みを DB で止めるため。** `EnsureRoomViaAP`
-- は uri で引いてから作るので、並行する Invite が同時に通ると 2 行できる。
-- `chat_room` は小さいので CONCURRENTLY は使わない (あれは単一文でしか実行できず、
-- backfill と同じ migration に置けない)。
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_chat_room_uri" ON "chat_room" ("uri") WHERE "uri" IS NOT NULL;
CREATE INDEX IF NOT EXISTS "IDX_chat_room_host" ON "chat_room" ("host") WHERE "host" IS NOT NULL;
