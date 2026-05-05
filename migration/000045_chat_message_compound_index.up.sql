-- chat_message の (fromUserId, toUserId) 複合 index 追加 (#711)。
--
-- 用途:
--   1. MarkAllReadFromUser (DM unread 一括既読化) — timeline open のたびに
--      実行される high-traffic path。WHERE "fromUserId" = ? AND "toUserId" = ?
--      で絞り込む。
--   2. ListMessagesByUser — 1-on-1 conversation の message 一覧。
--      WHERE ("fromUserId" = ? AND "toUserId" = ?) OR ("fromUserId" = ? AND
--      "toUserId" = ?) を 2 回走らせる plan を高速化。
--   3. SearchMessages — text ILIKE と組み合わせた DM 検索。
--
-- 既存の単一 index "IDX_chat_message_fromUserId" は他クエリ
-- (例: ListHistory の DISTINCT ON 経路で fromUserId だけで絞るパス) で
-- 引き続き有効なので残す。複合 index と prefix で重複するが index size の
-- overhead より plan 安定性を優先する。
--
-- 否定 array containment (NOT (reads @> ARRAY[?]::varchar[])) は GIN でも
-- 高速化できないため reads 用 index は追加しない。
CREATE INDEX IF NOT EXISTS "IDX_chat_message_fromUserId_toUserId"
    ON "chat_message" ("fromUserId", "toUserId");
