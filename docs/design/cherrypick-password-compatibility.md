# CherryPick password互換

Issue: https://github.com/shiroha-a/mk/issues/2838

CherryPick移行accountはArgon2id、mk-go accountはbcryptを保持し得る。signinとpassword変更は両形式をprefix dispatchで検証するが、新しいhashはmk-go標準bcryptだけを生成する。

受理するArgon2idはCherryPickの固定profile (`v=19`, `m=65536`, `t=3`, `p=4`, salt 16 byte, digest 32 byte) に限定する。DB埋め込みparameterを無制限に実行しない。

Argon2idの自動移行はCAPTCHA・2FAを含むsignin完了後だけ行う。観測済みhashを条件にしたCASで並行password変更を上書きせず、失敗はsigninを妨げない。72 byte超passwordはsigninを許可し、bcrypt化だけを保留する。

互換対象は`/api/signin`、`/api/signin-flow`、`/api/i/change-password`に限定する。他のpassword確認endpointと既存bcrypt rehashは変更しない。
