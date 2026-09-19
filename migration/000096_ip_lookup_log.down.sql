-- #3106 の巻き戻し。
--
-- data loss: 監査記録はすべて失われる。mk-go が作ったテーブルなので、TS へ
-- 戻すときに相当するものは無い。
DROP TABLE IF EXISTS "ip_lookup_log";
