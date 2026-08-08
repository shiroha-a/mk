-- data loss: 生成済みの秘密値が失われる。media proxy の HMAC 鍵を落とすと、
-- 発行済みの署名付き URL は検証に失敗する。ただし Authorize は署名検証に失敗しても
-- allowlist へフォールバックするため、avatar / drive / emoji / instance icon の
-- 配信は継続する。次回起動時に新しい鍵が生成される。
DROP TABLE IF EXISTS "instance_secret";
