-- UNIQUE index を削除する。up で削除した重複 note 行は復元できない (data loss)。
DROP INDEX IF EXISTS "IDX_note_uri";
