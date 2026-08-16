-- 承認制の申請を自己完結型にする (#2568 / #2569)。
--
-- 連絡先 (他サーバーのアカウントを MiAuth で確認したもの) を廃し、申請時に発行する
-- クレームコードで本人性を担保する形に置き換える。**外部サーバーに一切依存しなく
-- なる** — MiAuth は相手サーバーに access_token 行と通知を残し、こちらからは消せ
-- なかった (`i/revoke-token` は secure:true でネイティブトークンからしか呼べない)。
--
-- data loss: **既存の申請行はすべて削除する。** 連絡先で識別していた申請にコードを
-- 後から与える方法が無く (申請者に伝える経路が無い)、NOT NULL の claimCodeHash を
-- 埋められない。承認制はまだ運用に出していないため実害は無い想定だが、運用中で
-- あれば申請者に再申請してもらう必要がある。
DELETE FROM "signup_application";

-- 連絡先という自然キーが消えるので、重複申請を抑止していた部分一意インデックスも
-- 用途を失う。以降は captcha とレート制限が防波堤になる。
DROP INDEX IF EXISTS "IDX_signup_application_live_contact";

ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "contactHost";
ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "contactRemoteId";
ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "contactUsername";

-- **平文では持たない。** 平文で保存すると、DB が漏れた時点で全申請が乗っ取れる
-- (コードは状態確認と登録の両方の入口になる)。既存の registration_ticket は平文
-- だが、こちらは新規なので最初から hash で持つ。sha256 の hex なので 64 文字。
ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "claimCodeHash" varchar(64) NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_signup_application_claimCodeHash"
    ON "signup_application" ("claimCodeHash");
