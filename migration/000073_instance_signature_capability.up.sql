-- instance_signature_capability: リモートインスタンスがどの署名方式に対応して
-- いるかを host 単位で記録する mk-go 独自テーブル。
--
-- 判定材料は 3 系統あり、それぞれ単独では穴があるので併記する:
--
--   1. 宣言   — actor の assertionMethod[] (FEP-521a Multikey) に Ed25519 鍵が
--               あった = 対応表明。ただし表明しても実際に使うとは限らない。
--   2. 受信観測 — inbound HTTP Signature の verify に成功した鍵の種別。実際に
--               飛んできた方式だが、相手が Ed25519 対応でも RSA を選べば見えない。
--   3. 配送観測 — Ed25519 で署名した配送が 2xx を返した。ただし mk-go 自身を含む
--               verify-in-worker な実装は署名検証より先に 202 を返すため、これは
--               「検証できた」証拠ではなく「同期的に拒否されなかった」まで。
--               こちらから配送していない相手には残らない。
--
-- instance への FK は張らない。観測と instance 行の作成に順序保証が無く、FK は
-- 削除順序の制約を増やすだけで得が無い (instance_secret と同じ判断)。
--
-- TS は本テーブルを認識せず SELECT/INSERT 共に行わないので、TS へ戻しても壊れない
-- (instance_secret / user_publickey_extra と同じ扱い)。
CREATE TABLE IF NOT EXISTS "instance_signature_capability" (
    -- instance.host と対応する。instance 側は host が uniqueIndex なので host を
    -- そのまま PK にできる (id 経由の間接参照より lookup が素直)。
    "host"              varchar(128) PRIMARY KEY,

    -- 1. 宣言
    "ed25519DeclaredAt" timestamp with time zone,

    -- 2. 受信観測。inboundAlg は署名 header が名乗った algorithm 文字列ではなく
    --    「検証に成功した鍵の種別」を入れる (algorithm は "" / hs2019 がありうる
    --    ため、名乗りをそのまま信じると実態とずれる)。
    "inboundAlg"        varchar(32),
    "inboundObservedAt" timestamp with time zone,
    "ldSignatureSeenAt" timestamp with time zone,

    -- 3. 配送観測
    "ed25519AcceptedAt" timestamp with time zone,

    "updatedAt"         timestamp with time zone NOT NULL DEFAULT now()
);
