-- data loss: この migration より後に取り込んだリモート room は、ローカルで採番した
-- `id` を持ち、origin の room id を `uri` にしか持っていない。列を落とすと
-- **その対応が失われる**ので、それらの room は AP 上の身元を失う。配送側は
-- 「URI が分からない room」として配送を止め (`roomAPURI` が空を返す)、受信側は
-- 引き当てに失敗して恒久的に drop する。
--
-- migration より前からある room (id が origin の room id と同じもの) は、up 側の
-- backfill が同じ導出で埋め直すので戻せる。
DROP INDEX IF EXISTS "IDX_chat_room_host";
DROP INDEX IF EXISTS "IDX_chat_room_uri";
ALTER TABLE "chat_room" DROP COLUMN IF EXISTS "uri";
ALTER TABLE "chat_room" DROP COLUMN IF EXISTS "host";
