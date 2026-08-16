-- signup_application: 承認制の登録における「申請」を表す mk-go 独自テーブル (#2555)。
--
-- **承認待ちを `user` 行として持たないための箱。** `user` に mk-go 独自の承認列を
-- 足す設計にすると、TS に切り替えた瞬間に承認待ち全員が有効なアカウントになる
-- (TS はその列を知らないので素通りする)。承認制の意味が消えるうえ、気づく手段が
-- 無い。申請をこのテーブルに閉じ込めれば、TS は未知のテーブルを無視するだけで済む。
--
-- 副次的にユーザー名の先取り問題も消える。承認まで `user` を作らないので、名前は
-- 登録時に選ぶことになり、保留期間そのものが存在しない。
--
-- user への FK は張らない。`processedById` / `usedById` はどちらも監査用の記録で、
-- 参照先が消えても申請の履歴としては意味を保つ。FK を張ると管理者アカウントの削除が
-- 申請履歴に引っかかるだけで得が無い (instance_secret と同じ判断)。
CREATE TABLE IF NOT EXISTS "signup_application" (
    "id"              varchar(32) PRIMARY KEY,

    -- 連絡先。**username を鍵にしない** — 相手サーバーでの改名で壊れ、本人なのに
    -- 登録できなくなる。MiAuth の `check` 応答は相手サーバーのローカルユーザーを
    -- 返すので `uri` が null であり、安定して使えるのは (host, そのサーバー内の id)
    -- の組だけ。`contactUsername` は表示専用で、一致判定には使わない。
    "contactHost"     varchar(128) NOT NULL,
    "contactRemoteId" varchar(32)  NOT NULL,
    "contactUsername" varchar(128) NOT NULL,

    -- pending / approved / rejected / expired / completed
    "status"          varchar(16)  NOT NULL DEFAULT 'pending',
    "reason"          varchar(2048),

    -- 承認時に発行した registration_ticket。コードは利用者に渡さず、登録時に
    -- サーバー内部で消費する。
    "ticketId"        varchar(32),

    "createdAt"       timestamp with time zone NOT NULL DEFAULT now(),
    "updatedAt"       timestamp with time zone NOT NULL DEFAULT now(),

    -- 申請の有効期限。**短くしないこと** — 相手インスタンスが一時的に落ちている
    -- だけの場合が多く、1 日だと復旧すれば通ったはずの人が期限切れで落ちる。
    "expiresAt"       timestamp with time zone NOT NULL,

    -- 監査用。誰がいつ審査したか、最終的に誰が登録したか。
    "processedById"   varchar(32),
    "processedAt"     timestamp with time zone,
    "usedById"        varchar(32)
);

-- 同一連絡先で申請が二重に生きることを防ぐ。
--
-- **status を条件に含めるのが要点。** 全体に一意制約を張ると、却下・期限切れの
-- 後に同じアカウントで申請し直せなくなる。逆に条件を付けないと、審査中の申請を
-- 持ったまま何度でも積み増せる。
--
-- `completed` を条件に含めないのは意図的で、同じ連絡先からの 2 つ目の申請を
-- DB では禁じない。可否は審査する側の判断に委ねる。
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_signup_application_live_contact"
    ON "signup_application" ("contactHost", "contactRemoteId")
    WHERE "status" IN ('pending', 'approved');

-- 管理画面の一覧 (審査待ちを新しい順に出す)。
CREATE INDEX IF NOT EXISTS "IDX_signup_application_status_createdAt"
    ON "signup_application" ("status", "createdAt" DESC);
