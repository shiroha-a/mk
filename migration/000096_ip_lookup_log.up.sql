-- #3106: IP 照会そのものを監査する。
--
-- **`moderation_log` には入れない。** あちらは保持期間を持たず永久に残るのに、
-- ここに書くのは**照会に使った IP そのもの**で、IP とアカウントの対応と同じだけ
-- 機密性がある。`moderation_log` 全体に保持期間を入れると無関係な記録まで消えるので、
-- 専用のテーブルを持たせて日次の掃除で刈る (`iplookuplog.Retention`)。
--
-- **読み取りも専用の口にする。** `admin/show-moderation-logs` は管理者限定だが、
-- この記録は「誰が IP を引いたか」の一覧そのものなので、照会と同じ 3 段
-- (moderator + `canSearchIpHistory` + `read:admin:user-ips`) に揃える。
-- policy の既定は false で管理者は policy を bypass するため、**既定の実効は
-- 管理者のみ**。ロールで policy を配ればモデレーターも読める。
CREATE TABLE IF NOT EXISTS "ip_lookup_log" (
	"id" varchar(32) NOT NULL,
	-- 照会した人。**削除しても行は残す** (FK を張らない) — 監査の目的は
	-- 「誰が引いたか」を後から追えることで、退会で消えると目的を果たさない。
	-- 引けない利用者は読み取り側が「削除済み」として出す。
	"userId" varchar(32) NOT NULL,
	-- 'ip' (IP を直接指定、#3104) / 'relatedAccounts' (利用者起点、#3105) /
	-- 'userIps' (upstream の `admin/get-user-ips`)。
	"kind" varchar(32) NOT NULL,
	-- **照会に使った IP の正規形。** 利用者起点の照会では空文字。
	-- 列を分けないのは、どちらも「何を引いたか」で監査上は同じ役割のため。
	"ip" varchar(128) NOT NULL DEFAULT '',
	-- 利用者起点の照会で対象にした利用者。IP 起点では空文字。
	"targetUserId" varchar(32) NOT NULL DEFAULT '',
	-- 照会した期間 (日)。「いつの記録を見たか」も監査の対象。
	-- **'userIps' では 0**。あちらは窓を取らず最新 30 件を返すため。
	"sinceDays" integer NOT NULL,
	-- **結果の件数だけを残す。** 結果に含まれる IP やアカウントは複製しない
	-- (複製すると、この表が第 2 の「IP とアカウントの対応」になる)。
	"resultCount" integer NOT NULL,
	"createdAt" timestamp with time zone NOT NULL,
	CONSTRAINT "PK_ip_lookup_log" PRIMARY KEY ("id")
);

-- 監査の一覧は新しい順に引く。保持期間の刈り取りも同じ列を見る。
CREATE INDEX IF NOT EXISTS "IDX_ip_lookup_log_createdAt" ON "ip_lookup_log" ("createdAt" DESC);
