-- user_keypair_extra: local user の Ed25519 鍵ペアを保存する mk-go 独自テーブル。
-- 既存 user_keypair (RSA) は upstream Misskey TS との drop-in 互換のため untouched
-- とし、Ed25519 用は別テーブルに分離する。TS は本テーブルを認識せず SELECT/INSERT
-- 共に行わないので、TS swap 時にも壊れない (#1067 / #1068)。
CREATE TABLE IF NOT EXISTS "user_keypair_extra" (
    "userId"            varchar(32) PRIMARY KEY REFERENCES "user"("id") ON DELETE CASCADE,
    "ed25519PublicKey"  varchar(256) NOT NULL,
    "ed25519PrivateKey" varchar(256) NOT NULL
);
