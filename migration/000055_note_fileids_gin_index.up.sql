-- #1427 (perf audit finding 4): drive/files/attached-notes (ListByFileID,
-- note.go:652) は note.fileIds に対する array containment
-- ("fileIds" @> ARRAY[?]::varchar[]) で検索するが GIN index が無く note 全行
-- seq scan になっていた。index scan 化する。
--
-- CONCURRENTLY 採用理由・別 migration 分割の経緯、および失敗時の回復手順
-- (INVALID index の DROP + migrate dirty 解除) は 000054 と同じ (#1427)。
-- 対象 index 名はこちらは "IDX_note_fileIds"。
-- note は最大テーブルのため書き込みを block しない CONCURRENTLY で構築する。
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_note_fileIds" ON "note" USING gin ("fileIds");
