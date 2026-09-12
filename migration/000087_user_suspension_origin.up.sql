-- 凍結の由来を記録する (#2973)。Misskey TS に対応するテーブルは無いので mk-go
-- 独自の追加。
--
-- **コアテーブル (`user`) に列を足すのではなく別テーブルにする。** TS は未知の
-- 列も無視するので列追加でも復路は壊れないが、別テーブルなら TS 側から一切
-- 見えないため drop-in の面で更に安全。`user` は連合・認証・API のあらゆる
-- 経路が触るホットテーブルでもあり、そこへの変更は避けたい
-- (`relay_observed_user` / `signup_application` と同じ判断)。
--
-- 用途は「モデレーターの判断をリモートに巻き戻させない」こと。#2951 で
-- リモート actor の `toot:suspended` を読むようにしたが、由来を持たないため
-- **発信元が立て続けている間は `admin/unsuspend-user` の結果が次の actor
-- refresh で無言で戻る**。Mastodon は `suspension_origin` (local / remote) で
-- これを解いており (`ProcessAccountService#set_suspension!` の最初の 1 行が
-- `return if @account.suspended? && @account.suspension_origin_local?`)、
-- 同じ形にする。
CREATE TABLE IF NOT EXISTS "user_suspension_origin" (
	"userId" varchar(32) PRIMARY KEY,
	-- 'local' = モデレーターの判断。'remote' = 発信元の actor が立てた。
	"origin" varchar(16) NOT NULL,
	"updatedAt" timestamp with time zone NOT NULL DEFAULT now(),
	CONSTRAINT "FK_user_suspension_origin_userId"
		FOREIGN KEY ("userId") REFERENCES "user"("id") ON DELETE CASCADE
);

-- **既存の凍結済みユーザーは local 由来として扱う。**
--
-- この表が無かった時期の凍結には、モデレーターが操作したものと #2951 の自動
-- 凍結が混ざっている。区別する材料が無いので、**安全側 (= リモートに触らせない)
-- に倒す**。誤って local にした行は「発信元が解除してもこちらは凍結のまま」に
-- なるだけで、モデレーターが手で戻せる。逆に誤って remote にすると、モデレーターの
-- 判断がリモートに取り消されるので回復しにくい。
INSERT INTO "user_suspension_origin" ("userId", "origin", "updatedAt")
SELECT "id", 'local', now() FROM "user" WHERE "isSuspended" = true
ON CONFLICT ("userId") DO NOTHING;
