-- user_publickey_extra: リモート user の追加公開鍵 (Ed25519 等) を保存する mk-go
-- 独自テーブル。actor JSON の assertionMethod[] (FEP-521a Multikey) から parse した
-- 鍵を keyId 単位で保持し、HTTP Signature の verify と outbound deliver 時の
-- capability 判定に使う。既存 user_publickey (RSA, single row per userId) は untouched
-- とし、TS drop-in 互換を保つ (#1067 / #1068)。
CREATE TABLE IF NOT EXISTS "user_publickey_extra" (
    "userId" varchar(32)   NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "keyId"  varchar(256)  NOT NULL,
    "keyPem" varchar(4096) NOT NULL,
    "alg"    varchar(32)   NOT NULL,
    PRIMARY KEY ("userId", "keyId")
);
CREATE INDEX IF NOT EXISTS "IDX_user_publickey_extra_keyId"
    ON "user_publickey_extra" ("keyId");
