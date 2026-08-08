-- instance_secret: インスタンスごとに生成する秘密値を保存する mk-go 独自テーブル。
--
-- media proxy の HMAC 鍵が最初の用途。設定ファイルに mediaProxySecret が無い場合、
-- 以前はインスタンス URL から `sha256(url + "|mediaproxy")` を導出していたが、URL は
-- 公開情報なので誰でも同じ鍵を計算でき、署名を偽造して allowlist を迂回できた。
--
-- 鍵はプロセス間・再起動をまたいで安定している必要がある (署名した URL を別プロセス
-- が検証する / 発行済み URL が再起動後も有効である)。そのため起動時のメモリ生成では
-- 足りず、永続化する。
--
-- TS は本テーブルを認識せず SELECT/INSERT 共に行わないので、TS へ戻しても壊れない
-- (user_keypair_extra と同じ扱い)。
CREATE TABLE IF NOT EXISTS "instance_secret" (
    "key"       varchar(128) PRIMARY KEY,
    "value"     text        NOT NULL,
    "createdAt" timestamp with time zone NOT NULL DEFAULT now()
);
