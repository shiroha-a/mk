-- 000053 は backfill のみで schema 変更を伴わないため、down では復元せず
-- no-op で済ます。元の NULL に戻すと #1415 の mass notification を再度誘発
-- してしまうため、意図的に巻き戻さない。
SELECT 1;
