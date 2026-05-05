# デプロイ

## Docker Compose (TCP)

最も簡単な起動方法。PostgreSQL、Redis、mk-goの3サービスをTCPで接続する。

```bash
git clone --recursive https://github.com/shiroha-a/mk.git
cd mk

# フロントエンドビルド (初回のみ、3-10分)
make e2e-frontend-build

# 起動
docker compose up -d

# http://localhost:3000 でアクセス
```

`.config/docker.yml.example` が Dockerfile に焼き込まれ、`docker-compose.yml` の DB/Redis 既定値と整合した状態で起動するため、**初期設定なしで `docker compose up` だけで動く**。

設定をカスタマイズしたい場合は example をコピーして編集 + volume mount で上書き:

```bash
cp .config/docker.yml.example .config/docker.yml
# .config/docker.yml を編集 (URL / mediaProxy / etc.)
# docker-compose.yml の volumes コメントを外す:
#   - ./.config/docker.yml:/app/.config/default.yml:ro
docker compose up -d
```

`docker-compose.yml`の構成:
- **app**: mk-goコンテナ (ポート3000)
- **db**: PostgreSQL 16 Alpine
- **redis**: Redis 7 Alpine

ファイルストレージは`./files`にマウントされる。

> **注意**: コンテナは **UID/GID 991** (Misskey TS と同じ) で起動するため、ホスト側の `./files` ディレクトリは UID 991 が書き込めるパーミッションでなければならない。Misskey TS から移行する場合は既に 991 所有なのでそのままで OK。**今まで mk-go の旧 root 構成で運用していて初めて 991 化に追従する場合は、一度だけ `sudo chown -R 991:991 ./files` で所有権を揃える必要がある**。

### prebuilt imageについて

`ghcr.io/shiroha-a/mk:latest`等のprebuilt imageにはGoバイナリとマイグレーションSQLのみが含まれ、フロントエンドアセットは同梱されていない。prebuilt imageを使用する場合は以下の環境変数でアセットディレクトリを指定する必要がある:

- `MISSKEY_FRONTEND_DIR` — viteビルド出力
- `MISSKEY_FRONTEND_DIST_DIR` — dist出力 (locales, fonts)
- `MISSKEY_TWEMOJI_DIR` — twemoji SVG
- `MISSKEY_CLIENT_ASSETS_DIR` — クライアントアセット
- `MISSKEY_STATIC_DIR` — 静的ファイル (backend/assets: favicon等)
- `MISSKEY_REPO_ASSETS_DIR` — リポジトリ直下の共通アセット (ai.png, banner等)

TS版Misskeyのイメージからアセットをコピーすることも可能:

```dockerfile
FROM misskey/misskey:2026.3.2 AS misskey-assets
FROM ghcr.io/shiroha-a/mk:latest
COPY --from=misskey-assets /misskey/built /frontend
COPY --from=misskey-assets /misskey/packages/frontend/assets /client-assets
COPY --from=misskey-assets /misskey/packages/backend/node_modules/@discordapp/twemoji/dist/svg /twemoji
COPY --from=misskey-assets /misskey/packages/backend/assets /static
COPY --from=misskey-assets /misskey/assets /repo-assets
ENV MISSKEY_FRONTEND_DIR=/frontend/_frontend_vite_
ENV MISSKEY_FRONTEND_DIST_DIR=/frontend/_frontend_dist_
ENV MISSKEY_TWEMOJI_DIR=/twemoji
ENV MISSKEY_CLIENT_ASSETS_DIR=/client-assets
ENV MISSKEY_STATIC_DIR=/static
ENV MISSKEY_REPO_ASSETS_DIR=/repo-assets
```

## Docker Compose (UDS)

本番向け構成。UNIX Domain Socketのみで通信し、TCPポートの露出を最小化する。

```
nginx:80 → /run/mkgo/mkgo.sock → mk-go → /var/run/postgresql + /run/valkey/valkey.sock
```

```bash
# フロントエンドビルド
make uds-frontend-build

# 起動
make uds-up

# 確認
curl -i http://localhost/
```

詳細は[UDSデプロイ](docker-uds.md)を参照。

## バイナリ直接実行

```bash
# 設定ファイルを example から複製 (初回のみ)
cp .config/default.yml.example .config/default.yml
# 必要に応じて .config/default.yml を編集

# ビルド
make build

# マイグレーション適用
export DATABASE_URL="postgres://user:pass@localhost:5432/misskey?sslmode=disable"
make migrate-up

# 起動
./built/misskey -config .config/default.yml
```

前提条件: Go 1.26+ (ビルド時)、PostgreSQL 16+、Redis 7+。

設定ファイルの詳細は[設定リファレンス](configuration.md)を参照。

## systemdユニット例

```ini
[Unit]
Description=mk-go Misskey Backend
After=network.target postgresql.service redis.service

[Service]
Type=simple
User=misskey
WorkingDirectory=/opt/misskey
ExecStart=/opt/misskey/misskey -config /opt/misskey/.config/default.yml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## フロントエンド配信

mk-goはMisskeyのSPAフロントエンドをそのまま配信する。フロントエンドは`third_party/misskey`サブモジュールからビルドする。

環境変数でアセットディレクトリを指定:

| 環境変数 | 内容 |
|---|---|
| `MISSKEY_FRONTEND_DIR` | viteビルド出力 (`built/_frontend_vite_`) |
| `MISSKEY_FRONTEND_DIST_DIR` | dist出力 (`built/_frontend_dist_`) |
| `MISSKEY_CLIENT_ASSETS_DIR` | クライアントアセット (`packages/frontend/assets`) |

## 逆プロキシ (nginx)

### TCP構成

```nginx
upstream mkgo {
    server 127.0.0.1:3000;
}

server {
    listen 443 ssl;
    server_name misskey.example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    client_max_body_size 512M;
    proxy_read_timeout 1d;
    proxy_send_timeout 1d;

    location / {
        proxy_pass http://mkgo;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_redirect off;
    }
}
```

**注意点:**
- `client_max_body_size`はmk-goの`maxFileSize`設定 (デフォルト250MB) 以上に設定する
- `proxy_read_timeout 1d`はWebSocket (`/streaming`)のために必要
- `Upgrade`/`Connection`ヘッダーはWebSocketパススルーに必要

### UDS構成

```nginx
upstream mkgo {
    server unix:/run/mkgo/mkgo.sock;
}
```

upstream以外の設定はTCP構成と同じ。

## TS版からの移行

既存のMisskey (TypeScript版)からの移行手順は[TS版からの移行ガイド](migration-from-ts.md)を参照。

mk-goはTS版と同じPostgreSQL/Redisを共有できるため、バイナリの差し替えだけで移行可能。マイグレーションはTS版テーブルに対して追加のみで破壊的変更を行わない。

## 運用上の注意

### admin/overview の federation pie chart が一時的にずれる場合

`instance.followersCount` / `instance.followingCount` は Follow / Unfollow / Block→自動 unfollow に応じて incremental に維持される (#596) が、以下のような **bulk 操作** は incremental hook を経由しないので drift する:

- `admin/delete-account` でアカウントを大量削除 (`followingRepo.DeleteAllByUser` 経路)
- DB を直接操作した場合
- 起動時に並走する race による微小なズレ

drift は起動時の `RecomputeFollowCounts` で完全に再計算されるため、admin dashboard の federation pie chart に違和感が出たら **mk-go プロセスを再起動** すれば即時整合する。再起動以外で recompute を強制する API はまだ無い (将来 admin endpoint 化を検討)。
