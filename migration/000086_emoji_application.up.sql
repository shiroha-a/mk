-- emoji_application: カスタム絵文字の登録申請を表す mk-go 独自テーブル (#2934)。
--
-- **upstream には無い。** 絵文字の追加は `canManageCustomEmojis` を持つ人だけの
-- 操作で、一般の利用者から頼む導線が存在しなかった。運用では「Misskey の DM で
-- モデレーターに画像を送る」のような外部手段に落ちており、申請の取りこぼしも
-- 経緯の記録も残らない。
--
-- **承認までは `emoji` 行を作らない。** 承認待ちを `emoji` に持たせて隠す設計に
-- すると、TS に切り替えた瞬間に未承認の絵文字が全部有効になる (TS は mk-go 独自の
-- 列を知らないので素通りする)。`signup_application` (#2555) が `user` に対して
-- 同じ理由で採った形に揃える。
--
-- **kind は #2935 (リモート絵文字のインポート申請) と共有するために持つ。**
-- 審査する側が見る場所を 2 つに増やさないため、申請は種別を問わず 1 テーブルに
-- 入れる。'own' は自作画像で `fileId` を、'remote' はリモートの絵文字で
-- `remoteHost` / `remoteName` を使う。本 migration の時点で発行されるのは
-- 'own' だけ。
CREATE TABLE IF NOT EXISTS "emoji_application" (
    "id"          varchar(32) PRIMARY KEY,

    "userId"      varchar(32) NOT NULL,

    -- own / remote
    "kind"        varchar(16) NOT NULL DEFAULT 'own',
    -- pending / approved / rejected / canceled
    "status"      varchar(16) NOT NULL DEFAULT 'pending',

    -- 申請された絵文字の内容。承認時にそのまま emoji 行へ写す。
    --
    -- **name はここでは一意にしない。** 審査中に同じ名前の絵文字が別途登録される
    -- ことがあり、一意制約を張ると申請の保存時ではなく承認時に落ちる。重複は
    -- 承認の直前に見て、モデレーターに知らせる。
    "name"        varchar(128) NOT NULL,
    "category"    varchar(128),
    "aliases"     varchar(128)[] NOT NULL DEFAULT '{}',

    -- **申請ではライセンスを必須にする。** 通常の追加では任意だが、出典が後から
    -- 辿れないと、権利関係で問題が出たときに消すしか手が無くなる。
    "license"     varchar(1024) NOT NULL,

    "isSensitive" boolean NOT NULL DEFAULT false,

    -- kind = 'own'。drive のファイル。**承認するまで drive に置いたままにする** --
    -- 申請の時点で emoji として確定させないため。
    "fileId"      varchar(32),

    -- kind = 'remote' (#2935 で使う)。
    "remoteHost"  varchar(128),
    "remoteName"  varchar(128),

    -- 申請者からモデレーターへの補足。
    "comment"     varchar(2048),

    "createdAt"   timestamp with time zone NOT NULL DEFAULT now(),
    "updatedAt"   timestamp with time zone NOT NULL DEFAULT now(),

    -- 監査用。誰がいつ審査したか、却下ならその理由。理由は申請者にそのまま見せる。
    "processedById" varchar(32),
    "processedAt"   timestamp with time zone,
    "rejectReason"  varchar(2048),

    -- 承認して作られた emoji。**FK は張らない** (`signup_application` と同じ方針)
    -- ので、絵文字が削除されてもこの列は残る。監査としてはそのほうが都合がよい
    -- (「承認したが後で消された」経緯が辿れる)。参照するときは emoji 側の存在を
    -- 確かめること。
    "emojiId"     varchar(32)
);

-- 審査待ちの一覧。モデレーターが最も頻繁に引く。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_status_created"
    ON "emoji_application" ("status", "createdAt" DESC);

-- 自分の申請一覧。
CREATE INDEX IF NOT EXISTS "IDX_emoji_application_user_created"
    ON "emoji_application" ("userId", "createdAt" DESC);

-- 同じ人が同じ名前で審査待ちを積み増すことを防ぐ。
--
-- **status を条件に含めるのが要点。** 全体に一意制約を張ると、却下された後に
-- 直して出し直すことができなくなる。逆に条件を付けないと、審査中のまま何度でも
-- 同じ申請を送れてしまう。`signup_application` と同じ形。
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_emoji_application_pending_name"
    ON "emoji_application" ("userId", "name")
    WHERE "status" = 'pending';
