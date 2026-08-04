-- リレー経由で初めて観測した remote user を記録する (#2340)。Misskey TS に
-- 対応するテーブルは無いので mk-go 独自の追加。
--
-- **コアテーブル (`user`) に列を足すのではなく別テーブルにする。** TS は未知の
-- 列も無視するので列追加でも復路は壊れないが、別テーブルなら TS 側から一切
-- 見えないため drop-in の面で更に安全。`user` は連合・認証・API のあらゆる
-- 経路が触るホットテーブルでもあり、そこへの変更は避けたい。
--
-- 用途は孤児掃除の対象を「リレー経由で入ってきた行」に限定すること。印が無いと、
-- リレー購読前から居る行やプロフィール閲覧・スレッド遡りで解決された行まで
-- 巻き込んでしまう。
CREATE TABLE IF NOT EXISTS "relay_observed_user" (
	"userId" varchar(32) PRIMARY KEY,
	"observedAt" timestamp with time zone NOT NULL DEFAULT now(),
	CONSTRAINT "FK_relay_observed_user_userId"
		FOREIGN KEY ("userId") REFERENCES "user"("id") ON DELETE CASCADE
);

-- 掃除は observedAt が古い順に引くので index を張る。user 行が消えれば
-- CASCADE でこの行も落ちるため、孤児が残ることは無い。
CREATE INDEX IF NOT EXISTS "IDX_relay_observed_user_observedAt"
	ON "relay_observed_user" ("observedAt");

-- 掃除の設定。既定は無効で、既存インスタンスの挙動を変えない。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "enableRelayOrphanUserCleanup" boolean NOT NULL DEFAULT false;
-- 猶予日数。ResolveActor は actorTTL (24 時間) で lastFetchedAt を更新するため、
-- 短くすると活動中の著者が「削除 -> 再フェッチ」を繰り返すチャーンになる。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "relayOrphanUserGraceDays" integer NOT NULL DEFAULT 30;
