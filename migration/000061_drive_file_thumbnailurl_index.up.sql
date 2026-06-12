-- #1625: FindByAnyURL 用 index の 3/3 (経緯・CONCURRENTLY 採用理由・3 分割の
-- 事情は 000059 のコメント参照)。thumbnailUrl も nullable で実 query は
-- "thumbnailUrl" = ? のみのため、000060 と同じく部分 index にする。
CREATE INDEX CONCURRENTLY IF NOT EXISTS "IDX_drive_file_thumbnailUrl" ON "drive_file" ("thumbnailUrl") WHERE "thumbnailUrl" IS NOT NULL;
