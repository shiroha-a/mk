# デプロイ

## PostgreSQL 16 → 18 への移行 (既存環境)

compose 群と CI は PostgreSQL 18 に統一した (#2513)。**既存の 16 の data volume はイメージを上げるだけでは開けない** — メジャーアップグレードには dump→restore (または pg_upgrade) が必要で、そのまま起動すると `database files are incompatible` で crash loop になる。

さらに `postgres:18` イメージは **data layout が変わった** (default PGDATA が `/var/lib/postgresql/18/docker`、`VOLUME` 宣言が `/var/lib/postgresql` 親ディレクトリへ)。compose 群のマウント先はこれに合わせて `/var/lib/postgresql` に変更済み。旧パス (`/var/lib/postgresql/data`) のままイメージだけ上げると、**新規デプロイでは匿名 volume 側に initdb され、`down` で全データが静かに消える**。自前 compose を使っている場合はマウント先を確認すること。

既存環境の移行手順 (ダウンタイム = dump + restore の時間):

```bash
# 1. アプリを止めて書き込みを停止 (postgres は起動したまま)
docker compose stop mkgo

# 2. バックアップ (dump + volume の両方を取る)
docker compose exec -T db pg_dumpall -U <user> > pg16-dump.sql
docker run --rm -v <pg-volume>:/from -v "$PWD":/to alpine tar czf /to/pg16-data.tar.gz -C /from .

# 3. postgres を止め、volume を作り直して 18 で新規 init
docker compose down
docker volume rm <pg-volume>
docker compose up -d db          # ここで postgres:18 が新規 initdb する

# 4. restore (init が作った空 DB を落としてから dump を流す)
docker compose exec -T db psql -U <user> -d postgres -c 'DROP DATABASE <db>;'
docker compose exec -T db psql -U <user> -d postgres < pg16-dump.sql
docker compose exec -T db vacuumdb -U <user> --all --analyze-in-stages

# 5. アプリ再開・確認
docker compose up -d
```

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
- **db**: PostgreSQL 18 Alpine
- **redis**: Redis 7 Alpine

ファイルストレージは`./files`にマウントされる。

> **注意**: コンテナは **UID/GID 991** (Misskey TS と同じ) で起動するため、ホスト側の `./files` ディレクトリは UID 991 が書き込めるパーミッションでなければならない。Misskey TS から移行する場合は既に 991 所有なのでそのままで OK。**今まで mk-go の旧 root 構成で運用していて初めて 991 化に追従する場合は、一度だけ `sudo chown -R 991:991 ./files` で所有権を揃える必要がある**。

## Docker Compose (bundled image / ビルド不要)

フロントエンドアセットを同梱した `bundled` イメージを pull するだけで起動できる。**フロントエンドのビルドもイメージのビルドも要らない。**

動かすだけならソースを clone する必要は無い。compose と設定のひな形だけを置いた
[`docker` ブランチ](https://github.com/shiroha-a/mk/tree/docker)を使う (数十 KB)。

```bash
git clone --depth 1 -b docker https://github.com/shiroha-a/mk.git mk
cd mk

mkdir -p files && sudo chown -R 991:991 files
docker compose up -d
```

ソースを持っている場合は Makefile から同じことができる。

```bash
mkdir -p files && sudo chown -R 991:991 files

make image-up          # 起動 (docker-compose.image.yml)
make image-logs        # ログ
make image-down        # 停止
```

`url` を設定する場合は `.config/docker.yml` を用意し、`docker-compose.image.yml` の **`app` と `migrate` 両方**の volumes コメントを外す。マイグレーションは one-shot の `migrate` サービスが自動適用する。

手元の変更を反映したい場合は先にイメージを作る。

```bash
make image-build       # ghcr.io/shiroha-a/mk:bundled をローカルにビルド
```

ソースからビルドする従来の構成 (`docker-compose.yml` / `make docker-*`) はそのまま使える。こちらは置き換えではなく並立する選択肢。

### prebuilt imageについて

イメージは 2 種類ある。

| イメージ | 内容 | 用途 |
|---|---|---|
| `ghcr.io/shiroha-a/mk:bundled` | Goバイナリ + マイグレーション + **フロントエンドアセット同梱** | pull して即起動 |
| `ghcr.io/shiroha-a/mk:latest` | Goバイナリ + マイグレーションのみ | アセットを別途用意する構成 |

`bundled` / `latest` は develop の最新を指す **可変タグ**。本番ではバージョンを固定する。

```bash
MK_IMAGE=ghcr.io/shiroha-a/mk:1.1.1-bundled docker compose up -d
```

リリースタグを push すると `<version>` と `<version>-bundled` が publish される
(`.github/workflows/docker.yml`)。過去のリリースを後追いで publish したい場合は
develop に対して dispatch し、タグを input で渡す。

```bash
gh workflow run docker.yml -f tag=1.1.0
```

> `1.0.0` に `-bundled` は存在しない。アセット同梱イメージは 1.1.0 で追加された機能で、
> 1.0.0 のツリーには `Dockerfile.bundled` が無いため。

同梱アセットは fork (`shiroha-a/misskey-ts`) が publish する `ghcr.io/shiroha-a/misskey-ts-assets:<tag>` 由来で、**mk-go 独自のフロントエンド変更を含む**。

> **注意**: 下記のように upstream の `misskey/misskey` イメージからアセットをコピーする方法もあるが、その場合 **mk-go 独自のフロントエンド変更が失われる** (チャット・リバーシの連合が UI 上で「非対応」表示に戻る等)。drop-in 互換の検証目的でなければ `bundled` イメージを使うこと。

`ghcr.io/shiroha-a/mk:latest`等のprebuilt imageにはGoバイナリとマイグレーションSQLのみが含まれ、フロントエンドアセットは同梱されていない。prebuilt imageを使用する場合は以下の環境変数でアセットディレクトリを指定する必要がある:

- `MISSKEY_FRONTEND_DIR` — viteビルド出力
- `MISSKEY_FRONTEND_DIST_DIR` — dist出力 (locales, fonts)
- `MISSKEY_TWEMOJI_DIR` — twemoji SVG
- `MISSKEY_CLIENT_ASSETS_DIR` — クライアントアセット
- `MISSKEY_STATIC_DIR` — 静的ファイル (backend/assets: favicon等)
- `MISSKEY_REPO_ASSETS_DIR` — リポジトリ直下の共通アセット (ai.png, banner等)

TS版Misskeyのイメージからアセットをコピーすることも可能:

```dockerfile
FROM misskey/misskey:2026.7.0 AS misskey-assets
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

前提条件: Go 1.26+ (ビルド時)、PostgreSQL 18推奨 (16以降で動作、CI検証は18)、Redis 7+。

設定ファイルの詳細は[設定リファレンス](configuration.md)を参照。

## データ整合性チェック (fsck)

非正規化カウンタが実データとずれていないかを検査する。

```bash
./built/misskey -config .config/default.yml -fsck        # 検査のみ (既定)
./built/misskey -config .config/default.yml -fsck -fix   # カウンタを直す
```

```
  カウンタのずれ: 2 件
    user.followersCount      1 件
    user.notesCount          1 件

    user.followersCount  id=u1  記録 7 → 実際 0
    user.notesCount  id=u1  記録 99 → 実際 2

  修正するには -fix を付けて再実行してください。
```

**既定は読み取り専用**で、`-fix` を付けたときだけ書き戻す。ずれがあれば exit 1 を返す
(`-fix` で直せば 0)。

### 検査するもの

| 対象 | 突き合わせ先 |
|---|---|
| `user.followersCount` / `followingCount` | `following` の実件数 |
| `user.notesCount` | `note` の実件数 |
| `note.repliesCount` / `renoteCount` | `note.replyId` / `renoteId` の実件数 |
| 孤児行 | 存在しない user を参照する `note` / `drive_file` / `following` |

これらのカウンタは増減で維持されており、増減はベストエフォート (戻り値を捨てる呼び出しが
ある) なので失敗すればそのままずれる。

### 孤児行は自動削除しない

**報告に留める。** カウンタは元データから導けるので `-fix` で直せるが、**削除した行は
復元できない**。影響を確認した上で手動で対応する。

### 検査しないもの

`clippedCount` / `pageCount` は**意図的に対象外**。mk-go はクリップ件数の非正規化カウンタを
維持せず `clip_note` を直接数える設計なので、常に 0 が正しい値になる
([divergence.md](divergence.md) 参照)。実件数と突き合わせると全件がずれとして報告される。

## 設定の実効値を確認する

```bash
./built/misskey -config .config/default.yml -config-dump
```

**サーバーを起動しない**ので、DB / Redis に繋がらない状態でも使える。

出力は「設定値」(YAML + 環境変数 + 既定値を畳んだ結果) と「実効値」(設定値そのもの
ではないが実際に効く値) の 2 部構成。**後者が要点**で、設定ファイルを読んだだけでは
分からないものを出す。

```
実効値
  process role            both   (1 プロセスで HTTP とジョブの両方を担う)
  worker: deliver         4   (明示指定)
  worker: inbox           16   (既定値)
  rate: deliver           400 jobs/sec   (設定値 100 × worker 4。rate limit は worker ごとに効く)
  redis pool (job queue)  80   (poolSize 未指定時の自動サイジング結果)

注意
  - deliverJobPerSec は設定値 100 だが、worker が 4 なので実際の上限は 400 jobs/sec になる
```

`*JobPerSec` が worker 数で倍化する件は
[設定リファレンス](configuration.md)にも記載しているが、**自分のインスタンスで実際に
いくつになっているか**はここでしか分からない。

パスワード・鍵・proxy の認証情報はマスクされる。設定の有無だけは分かるようにしてある
(未設定と設定済みの区別は診断に要るため)。

## セルフ診断 (doctor)

構成の詰まりを一括で検査する。**サーバーが起動していなくても回せる**ので、新規構築時に
まず走らせると早い。

```bash
./built/misskey -config .config/default.yml -doctor
```

```
  ok    config.url   https://example.com
  ok    database     接続 ok / migration version 74
  ok    redis        接続 ok
  FAIL  webfinger    status 403 (連合が無効)
        インスタンス設定の `federation` が `none` になっている。連合するなら管理画面で有効にする
  ok    nodeinfo     mk-go 1.1.2
  warn  actor        assertionMethod (Ed25519) が無い
        RSA だけでも連合できる。Ed25519 を公開すると対応実装との署名検証が軽くなる
  ok    tls          証明書の残り 68 日
```

FAIL があれば exit 1 を返すので、デプロイスクリプトの検証段に組み込める。
warn は「見ておくべき」であって「壊れている」ではないため exit code には影響しない。

### 何を見ているか

| 検査 | 内容 |
|---|---|
| `config.url` | 絶対 URL か、https か |
| `database` | 接続と、`schema_migrations` が同梱マイグレーションに追いついているか |
| `redis` | 接続 |
| `webfinger` | **公開 URL 経由**で `acct:instance.actor@<host>` が引けるか |
| `nodeinfo` | discovery から本体まで辿れるか |
| `actor` | `application/activity+json` で返るか、`publicKey` / `assertionMethod` が載っているか、`id` の host が `url` と一致するか |
| `tls` | 証明書の残り日数 |

**要点は「内部から叩かない」こと。** リバースプロキシの転送漏れは内部からは正常に
見えるので、必ず `url` の外形 URL 経由で確かめる。

管理画面からは `admin/self-check` で同じ検査を実行できる (moderator 以上)。

## Web と配送を別ノードに分ける

既定では 1 プロセスが HTTP とジョブキューの両方を担う。連合配送のバースト負荷を
ユーザー向けレイテンシから切り離したい場合や、配送だけ独立にスケールさせたい場合は、
**環境変数で役割を分けられる** (upstream Misskey と同じ `MK_ONLY_SERVER` /
`MK_ONLY_QUEUE`)。

```bash
# Web ノード: HTTP を提供し、ジョブは積むだけで処理しない
MK_ONLY_SERVER=1 ./built/misskey -config .config/default.yml

# 配送ノード: ジョブを処理する。API は生えない
MK_ONLY_QUEUE=1 ./built/misskey -config .config/default.yml
```

**設定ファイルは両ノードで同じものを使う。** 同じ DB・同じ Redis を向けていることが
前提で、特に `redisForJobQueue` がずれていると Web が積んだジョブを配送ノードが永久に
拾わない。

`redisForPubsub` などの `redisFor*` は**役割の指定ではない**。あれは用途ごとの Redis
接続先で、Web ノードも `redisForJobQueue` を読んで enqueue する。

### 各ノードが担うもの

| 処理 | Web (`MK_ONLY_SERVER`) | 配送 (`MK_ONLY_QUEUE`) |
|---|---|---|
| HTTP API / フロントエンド / WebSocket | ○ | — |
| ジョブの enqueue | ○ | ○ |
| ジョブの処理 (deliver / inbox 等) | — | ○ |
| cron (chart 集計 / 期限切れ prune 等) | — | ○ |
| worker の auto-scale | — | ○ |
| `serverStats` / `queueStats` の配信 | ○ | — |
| chart のメモリバッファ flush | ○ | ○ |

配送ノードは `/healthz` だけ応答する (`enableMetrics` が真なら `/metrics` も)。
これにより `./built/misskey -healthcheck` と Dockerfile の healthcheck が
**どちらのノードでもそのまま使える**。

なお `-healthcheck` は `127.0.0.1:<port>` を叩くので、`socket` で UDS 運用している
場合は role に関係なく使えない (従来からの制限)。

### 注意

- **両方を同時に指定するとエラーで起動しない。** 矛盾した設定を黙って片方優先で
  流すと「配送が動かない」形で後から気付くことになるため
- 管理画面の `serverStats` は **Web ノードのホスト**の値しか出ない。配送ノードの
  CPU / メモリは別途 Prometheus (`enableMetrics`) 等で見る
- 配送ノードを 0 台にすると**ジョブが処理されない**。最低 1 台は必要

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

> **submodule bump 後の追従手順** (= 新 Misskey TS release を取り込んだ PR をマージした後):
> 詳細は [upstream-catch-up.md](./upstream-catch-up.md#1-既存環境への適用--submodule-bump-pr-マージ後) 参照。`git pull` だけでは submodule の working tree は更新されないため、`git pull --recurse-submodules` または `git submodule update --init --recursive` が必要。frontend asset は `make uds-frontend-build` で再ビルド。

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

### 上限を上げられない場合 (分割アップロード)

Cloudflareを経由する構成ではリクエストボディが**100MB (Free/Pro) を超えるとエッジで弾かれる**。mk-goに到達しないため`maxFileSize`をいくら上げても大きいファイルを送れない。

この場合は**分割アップロード**を有効にする。ファイルを一定サイズのチャンクに割って複数リクエストで送り、オブジェクトストレージのマルチパートアップロードで結合する。Cloudflare固有ではなく、`client_max_body_size`を上げられないリバースプロキシ全般に効く。

**オブジェクトストレージ (`useObjectStorage`) が必須。** ローカルストレージ構成では機能ごと無効になり、`/api/meta`の能力告知が出ないのでクライアントは従来の単発アップロードに倒れる。

設定は**コントロールパネル → オブジェクトストレージ**にある。

| 設定 | 既定 | 意味 |
|---|---|---|
| 分割アップロードを有効にする | `false` | インスタンス全体の有効/無効 |
| チャンクサイズ (MiB) | `10` | 5〜32。1リクエストの上限になる |
| セッションの有効期限 (分) | `60` | 5〜1440 |
| ユーザーあたりの同時セッション数 | `8` | ロールポリシーの上限をここで頭打ちにする |
| ユーザーあたりの未完了バイト数 (MiB) | `2048` | 同上 |

チャンクサイズはボディ上限だけでなく**タイムアウト**で決まる。Cloudflareは100秒でタイムアウトするため、上り5 Mbpsでは10 MiBが約17秒に対し100 MBは約160秒でタイムアウトする。既定の10 MiBは、S3の最小パートサイズ (5 MiB) の2倍かつCloudflareのボディ上限の1/10。`client_max_body_size`を上げられない環境ではこれを下げる。

ユーザーごとの可否と上限は**ロールポリシー**でも制御できる (`canUseChunkedUpload` / `chunkedUploadMaxConcurrentSessions` / `chunkedUploadMaxPendingMb`)。上の管理画面設定が上限になるので、ロールに大きい値を入れてもインスタンス設定は超えられない。

**バケット側にライフサイクルルールを設定すること。** オブジェクトストレージは**未完了のマルチパートアップロードにも課金する**。mk-goは期限切れセッションを15分ごとに`AbortMultipartUpload`で回収するが、これが動かない障害時の保険としてバケット側にも「incomplete multipart uploadを N 日で削除」を入れておく。

### UDS構成

```nginx
upstream mkgo {
    server unix:/run/mkgo/mkgo.sock;
}
```

upstream以外の設定はTCP構成と同じ。

## オブジェクトストレージ

**コントロールパネル → オブジェクトストレージ**で設定する。`meta` テーブルに保存されるため設定ファイルの編集も再起動も不要で、保存した時点から次のアップロードに反映される。

`objectStorageEndpoint` は**ホスト名だけ**を入れる。`https://` などのスキームやバケット名のパスを含めると、mk-go が `https://` を前置してエンドポイント URL を組むため不正な URL になる (本家 Misskey の `S3Service.getS3Client` も同じ組み立て方をする)。

| 項目 | 例 |
|---|---|
| エンドポイント | `s3.us-west-000.backblazeb2.com` / `<accountid>.r2.cloudflarestorage.com` |
| バケット名 | `misskey-drive` |
| Base URL | `https://files.example.com` (公開 URL のベース。スキーム必須) |
| プレフィックス | `files` |

保存されるファイルの公開 URL は `<Base URL>/<プレフィックス>/<アクセスキー>` になる。Base URL 側にプレフィックスを重ねると二重になるので注意。

### 有効化前に保存したファイル

オブジェクトストレージを有効にする前にアップロードされたファイルは、`drive_file.storedInternal = true` としてローカル FS (`./drive-files`) に残る。**これらは移動されない。** mk-go は有効化後もこの列を見て配信元を切り替えるので、既存ファイルはそのまま表示できる。

したがって、有効化したあともローカルの `drive-files` を消してはいけない。まとめてオブジェクトストレージへ移す機能は未提供。

### 無効化に戻す場合

無効化すると、有効化中に保存されたファイル (`storedInternal = false`) はオブジェクトストレージ側にあるままなので、バケットを消すと表示できなくなる。

## TS版からの移行

既存のMisskey (TypeScript版)からの移行手順は[TS版からの移行ガイド](migration-from-ts.md)を参照。

mk-goはTS版と同じPostgreSQL/Redisを共有できるため、バイナリの差し替えだけで移行可能。マイグレーションはTS版テーブルに対して追加のみで破壊的変更を行わない。

## アップデート

どの構成でも共通する原則は 3 つ。

1. **`git pull` だけでは submodule が更新されない**。親リポの gitlink ポインタが動くだけで `third_party/misskey/` の実ファイルは古いまま残る。`git pull --recurse-submodules` を使うか、`git config submodule.recurse true` を一度実行しておく
2. **submodule が動いたらフロントエンドを再ビルドする**。SPA のアセットは image に焼き込まず bind-mount で渡しているため、submodule だけ進めても配信物は変わらない
3. **フロントエンドを再ビルドしたら mk-go を再起動する**。エントリポイント (`scripts/<hash>.js`) を起動時に 1 回だけ解決してキャッシュする実装なので、再起動しないと消えた古いファイルを指し続けて 404 になる。bind-mount であっても再起動は必要

マイグレーションは構成によって適用方法が違う (下記参照)。golang-migrate が `schema_migrations` で適用済みバージョンを管理するため、何度流しても冪等。

### Docker Compose (TCP / UDS 共通)

```bash
git pull --recurse-submodules

# third_party/misskey が動いていた場合のみ
make e2e-frontend-build      # UDS 構成では make uds-frontend-build

docker compose build
docker compose up -d         # UDS 構成では make uds-build && make uds-up
```

マイグレーションは one-shot の `migrate` サービスが `app` の起動前に自動適用する。`docker compose up -d` が完了した時点で適用済み。

`.config/docker.yml` を volume mount で使っている場合、**`migrate` 側の mount も忘れずに維持する**こと。片方だけだとマイグレーションと本体が別の DB を見る。

### バイナリ直接実行

```bash
git pull --recurse-submodules

# third_party/misskey が動いていた場合のみ
make e2e-frontend-build

make build

# マイグレーションは手動適用 (compose と違い自動では走らない)
export DATABASE_URL="postgres://user:pass@localhost:5432/misskey?sslmode=disable"
make migrate-up

# 再起動
sudo systemctl restart misskey    # systemd の場合
```

### 切り戻し

`schema_migrations` のバージョンが進んでいるので、バイナリだけ戻すと古い mk-go が新しいスキーマを読むことになる。追加のみのマイグレーション (`ADD COLUMN` / `CREATE TABLE` / `CREATE INDEX`) であれば旧バイナリでも動くが、破壊的な変更を含むリリースでは `make migrate-down` で段階的に戻す必要がある。リリースノートで破壊的変更の有無を確認すること。

Misskey TS へ戻す場合は[TS版からの移行](migration-from-ts.md)を参照。

## 運用上の注意

### admin/overview の federation pie chart が一時的にずれる場合

`instance.followersCount` / `instance.followingCount` は Follow / Unfollow / Block→自動 unfollow に応じて incremental に維持される (#596) が、以下のような **bulk 操作** は incremental hook を経由しないので drift する:

- `admin/delete-account` でアカウントを大量削除 (`followingRepo.DeleteAllByUser` 経路)
- DB を直接操作した場合
- 起動時に並走する race による微小なズレ

drift は起動時の `RecomputeFollowCounts` で完全に再計算されるため、admin dashboard の federation pie chart に違和感が出たら **mk-go プロセスを再起動** すれば即時整合する。再起動以外で recompute を強制する API はまだ無い (将来 admin endpoint 化を検討)。
