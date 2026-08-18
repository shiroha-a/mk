-- #2623: 000080 以前の `ON DELETE SET NULL` が壊した行を後始末する。
--
-- FK があった頃は、元ノートが削除されるとリノート側の `renoteId` が NULL に
-- 書き換わった。`renoteUserId` には FK が無いので値が残り、
-- 「`renoteId` は無いのに `renoteUserId` はある」という、通常の投稿経路では
-- 生じない形の行が残っている。
--
-- **DDL とは別ファイルにしてある。** DROP CONSTRAINT が取る ACCESS EXCLUSIVE を
-- 全表スキャンの間ずっと保持しないようにするため (golang-migrate はファイルを
-- 1 つの Exec で送るので、暗黙の単一トランザクションになる)。

-- 1. 中身が何も残っていない行を削除する。
--
-- **この DELETE は復元できない。** `renoteId` が失われているため、どのノートへの
-- リノートだったかは残っていない。本文も添付も引用先も無いので、残しても
-- 利用者には空欄が見えるだけになる。
--
-- 条件は「元が pure renote で、対象が消えていて、**誰も触っていない**」に
-- 限定する。リアクション・返信・リノート・クリップが付いている行は、リンクが
-- 切れていても他利用者の操作の記録なので残す (note_reaction / note_favorite /
-- clip_note / user_note_pining などは note への FK が ON DELETE CASCADE なので、
-- 消すとそれらも道連れになる)。
--
-- **注意: この DELETE はアプリ層の後始末を通らない。**
-- core/note.DeleteService が持つ Redis fanout の LREM / chart / 検索インデックス /
-- ストリーム通知は動かないので、Redis の timeline list には削除済み ID が残る
-- (取得時に解決できず落ちるだけで実害は無いが、timeline JSON cache は TTL まで
-- 古いページを返しうる)。件数が多い環境では適用後に timeline cache を flush する
-- のが望ましい。
DELETE FROM "note"
WHERE "renoteId" IS NULL
  AND "renoteUserId" IS NOT NULL
  AND (text IS NULL OR text = '')
  AND (cw IS NULL OR cw = '')
  AND "replyId" IS NULL
  AND "fileIds" = '{}'
  AND "hasPoll" = false
  AND "reactions" = '{}'::jsonb
  AND "repliesCount" = 0
  AND "renoteCount" = 0
  AND "clippedCount" = 0;

-- 2. 残した行から、切れたリンクの痕跡を消す。
--
-- 本文や添付を持つ行 (引用など) は 1 で残るが、`renoteUserId` /
-- `renoteUserHost` / `renoteChannelId` は指す先が無いまま残っている。
-- **これらの列はミュート / ブロック / インスタンスミュートの判定に使われる**
-- (core/timeline の renote-author 判定、notesfilter の muteblock、SQL 側の
-- `renoteUserHost NOT IN (...)`)。放置すると、引用先が消えて何も参照していない
-- ノートが「その相手をミュートしている人には見えない」状態のまま固定される。
UPDATE "note"
SET "renoteUserId" = NULL, "renoteUserHost" = NULL, "renoteChannelId" = NULL
WHERE "renoteId" IS NULL AND "renoteUserId" IS NOT NULL;

-- 返信側も同じ理由で揃える (replyId が SET NULL で消え replyUserId だけ残る形)。
UPDATE "note"
SET "replyUserId" = NULL, "replyUserHost" = NULL
WHERE "replyId" IS NULL AND "replyUserId" IS NOT NULL;
