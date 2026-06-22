-- #2069 (upstream 2026.6.0 #17511 追従): user_note_pining.noteId 単一 index を追加。
-- upstream #17511 は note_favorite.noteId / user_note_pining.noteId に単一 index を
-- 足した。mk-go は note_favorite 側は 000017 で既に持つが、user_note_pining は
-- IDX("userId") と UNIQUE("userId","noteId") のみで、noteId 単独検索 (ノート削除時の
-- pin 逆引き等) に使える index が無い (UNIQUE は第 1 カラムが userId のため効かない)。
--
-- CONCURRENTLY は transaction 外・単一文でのみ実行できるため 1 文のみで構成する。
-- 失敗時の回復手順 (INVALID index の DROP + migrate dirty 解除) は 000054 の
-- コメントを参照 (対象 index 名は読み替える)。
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_user_note_pining_noteId" ON "user_note_pining" ("noteId");
