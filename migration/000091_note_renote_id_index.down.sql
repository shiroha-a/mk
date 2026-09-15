-- CONCURRENTLY で作った index は CONCURRENTLY で drop して write block を避ける
-- (DROP INDEX CONCURRENTLY も transaction 外・単一文でのみ実行可能)。
DROP INDEX CONCURRENTLY IF EXISTS "IDX_52ccc804d7c69037d558bac4c9";
