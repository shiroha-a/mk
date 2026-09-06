-- upstream の down と同じ形。複合 index を落として単独 index に戻す。
--
-- **index 名は mk-go 側の元の名前 (`IDX_note_userId`) に戻す。** up で落としたのも
-- こちらで、upstream の `IDX_5b87d9d19127bd5d92026017a7` は mk-go には元から無い。
DROP INDEX IF EXISTS "IDX_724b311e6f883751f261ebe378";
CREATE INDEX IF NOT EXISTS "IDX_note_userId" ON "note" ("userId");
