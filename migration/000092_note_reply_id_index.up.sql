-- #2995: `note."replyId"` に index を張る。理由と回復手順は 000091 と同じ
-- (CONCURRENTLY は単一文でしか実行できないので migration を分けてある)。
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_17cb3553c700a4985dff5a30ff" ON "note" ("replyId");
