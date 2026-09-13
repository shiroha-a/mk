-- モデレーターがカスタム絵文字の申請枠を手動でリセットできるようにする (#2962)。
--
-- **申請の行そのものは触らない。** 誤操作・テスト申請・運営からの再申請依頼で
-- 枠を戻したいとき、履歴を消して枠を空けると**過去の判断が追えなくなる**
-- (#2960 が「関連する過去の申請」を出すために持っている材料も同時に消える)。
-- 代わりに「いつリセットしたか」を持ち、期間内の件数を
-- `max(期間の開始日時, 最後のリセット日時)` 以降で数える。
--
-- **1 行 1 操作で積む。** 上書きにすると誰がいつ何回戻したかが残らず、
-- 監査の役に立たない。読むときは最新の 1 行を採る。
CREATE TABLE IF NOT EXISTS "emoji_application_quota_reset" (
	"id" varchar(32) NOT NULL,
	"userId" varchar(32) NOT NULL,
	"resetById" varchar(32) NOT NULL,
	-- **理由は必須。** 監査ログに残る唯一の文脈なので、空を許すと
	-- 「誰かが戻した」以上のことが分からなくなる。
	"reason" varchar(1024) NOT NULL,
	"createdAt" timestamp with time zone NOT NULL,
	CONSTRAINT "PK_emoji_application_quota_reset" PRIMARY KEY ("id")
);

-- **`resetAt` 列は作らない。** `createdAt` がその値そのもので、2 つ持つと
-- 片方だけ更新されたときに食い違う。
--
-- 最新の 1 行を引くための索引。申請のたびに引くので、利用者で絞って
-- 新しい順に 1 件取れる形にする。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_quota_reset_user"
	ON "emoji_application_quota_reset" ("userId", "createdAt" DESC);
