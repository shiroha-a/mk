-- 分割アップロード (#2313)。Misskey TS に対応する機能は無いので mk-go 独自の
-- 追加であり、upstream migration との対応も無い。TS へ切り戻しても未知の列 /
-- テーブルは無視されるだけなので drop-in の復路は壊れない。

-- 設定はコントロールパネル → オブジェクトストレージ に出す。分割アップロードは
-- S3 マルチパートに委譲するため、既存の objectStorage* 列と同じく meta で持つ。
-- 既定を false にするのは、リバースプロキシの client_max_body_size を確認せずに
-- 有効化すると必ず失敗するため (管理者が意図的に入れる形にする)。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "chunkedUploadEnabled" boolean NOT NULL DEFAULT false;
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "chunkedUploadChunkSizeMb" integer NOT NULL DEFAULT 10;
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "chunkedUploadSessionTtlMinutes" integer NOT NULL DEFAULT 60;
-- 下 2 つは role policy に対するサーバー cap。admin が role 側に大きい値を入れても
-- インスタンス設定を超えられないようにする (capServerMaxFileSize と同じ役割)。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "chunkedUploadMaxSessionsPerUser" integer NOT NULL DEFAULT 8;
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "chunkedUploadMaxPendingMbPerUser" integer NOT NULL DEFAULT 2048;

-- 進行中セッションの状態。S3 の UploadId はここでだけ保持し、クライアントには
-- 露出しない (クライアントが握るのは本テーブルの不透明な id のみ)。
--
-- "user" への FK は張らない。CASCADE で行だけ消えると S3 側の未完了マルチパート
-- アップロードが AbortMultipartUpload されないまま孤児になり、課金が残り続ける。
-- user 削除後も行を残し、期限切れ GC (clean cron) に abort させる。
CREATE TABLE IF NOT EXISTS "chunked_upload_session" (
    "id"             varchar(32)  NOT NULL,
    "createdAt"      timestamp with time zone NOT NULL,
    "updatedAt"      timestamp with time zone NOT NULL,
    "expiresAt"      timestamp with time zone NOT NULL,
    "userId"         varchar(32)  NOT NULL,
    "name"           varchar(256) NOT NULL,
    "comment"        varchar(512),
    "folderId"       varchar(32),
    "isSensitive"    boolean      NOT NULL DEFAULT false,
    "force"          boolean      NOT NULL DEFAULT false,
    -- クライアントの申告値。append の累計がこれを超えたら打ち切る。
    "totalSize"      bigint       NOT NULL,
    -- start 時点の meta 値を固定して持つ。途中で admin が設定を変えても
    -- 進行中セッションのパートサイズが変わらないようにする (R2 の均一
    -- パートサイズ要求を壊さないため)。
    "chunkSize"      integer      NOT NULL,
    "receivedBytes"  bigint       NOT NULL DEFAULT 0,
    "receivedChunks" integer      NOT NULL DEFAULT 0,
    "accessKey"      varchar(256) NOT NULL,
    -- 最初の append で先頭 512 バイトを sniff してから CreateMultipartUpload を
    -- 呼ぶため、それまでは NULL。
    "uploadId"       text,
    "contentType"    varchar(256),
    -- [{ "index": 0, "etag": "...", "size": 10485760, "sha256": "..." }, ...]
    "parts"          jsonb        NOT NULL DEFAULT '[]',
    -- finish の同時実行で DriveFile が二重作成されないようにする claim フラグ。
    "finishing"      boolean      NOT NULL DEFAULT false,
    "requestIp"      varchar(128),
    "requestHeaders" jsonb,
    CONSTRAINT "PK_chunked_upload_session" PRIMARY KEY ("id")
);

CREATE INDEX IF NOT EXISTS "IDX_chunked_upload_session_userId"
    ON "chunked_upload_session" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_chunked_upload_session_expiresAt"
    ON "chunked_upload_session" ("expiresAt");
