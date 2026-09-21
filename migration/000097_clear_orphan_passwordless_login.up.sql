-- 2FA を解除済みなのに `usePasswordLessLogin` が立ったままの行を落とす。
--
-- `i/2fa/unregister` が `twoFactorEnabled` だけを落としていたため、**登録済み
-- パスキーによるパスワード無しログインが通り続ける**行が残りうる。しかも
-- その状態では `securityKeysList` が `twoFactorEnabled` 分岐で空に潰れるので、
-- 利用者は鍵を見ることも消すこともできず自力で復旧できない (復旧経路は
-- モデレーターの `admin/unset-mfa` だけ)。
--
-- 解除側は既に直してあるので、ここで直すのは**それ以前に踏んだ行**。
UPDATE "user_profile"
SET "usePasswordLessLogin" = false
WHERE "twoFactorEnabled" = false
  AND "usePasswordLessLogin" = true;
