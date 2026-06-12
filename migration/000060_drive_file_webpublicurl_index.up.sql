-- #1625: FindByAnyURL 用 index の 2/3 (経緯・CONCURRENTLY 採用理由・3 分割の
-- 事情は 000059 のコメント参照)。webpublicUrl はリモートキャッシュや webpublic
-- 未生成の file で NULL になり、実 query は "webpublicUrl" = ? のみで NULL を
-- 引かないため、部分 index (WHERE IS NOT NULL) で NULL 行を除外して index を
-- 小さく保つ (000040 drive_file uri と同方針)。
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_drive_file_webpublicUrl" ON "drive_file" ("webpublicUrl") WHERE "webpublicUrl" IS NOT NULL;
