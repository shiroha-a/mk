# ActivityPub連合

## パッケージ構成

### `internal/activitypub/` (プロトコル層)

| ファイル | 責務 |
|---|---|
| `types.go` | ActivityStreams 2.0の型定義 (Object, Person, Note, Activity等) |
| `renderer.go` | Goモデル → AP JSON-LD変換 (RenderPerson, RenderNote, RenderFollow等) |
| `signature.go` | HTTP Signatures (RSA-SHA256, Cavage draft v12) の署名・検証 |
| `client.go` | AP HTTPクライアント (署名付きPOST/GET、リダイレクト制御) |
| `jsonld.go` | `@context`構築、JSON-LD正規化 (Mastodonプレフィックス互換) |
| `keypair.go` | RSA 2048bit鍵ペアの生成・PEM解析 |
| `mfm/` | MFM(Misskey Flavored Markdown) → HTML変換 |

### `internal/core/federation/` (ビジネスロジック層)

| ファイル | 責務 |
|---|---|
| `resolver.go` | リモートアクター/ノートの取得・永続化。公開鍵の2層キャッシュ (メモリ+DB, TTL 24h) |
| `deliver_service.go` | 配信ジョブのエンキュー。フォロワーのInbox収集、ホストブロック判定 |
| `processor.go` | 受信Activityのディスパッチ (Follow, Create, Like, Announce, Delete, Update等) |
| `published_time.go` | AP `published` を parse + clock skew (5min) / 過去 10 年 floor で fallback (#940) |
| `remote_stats.go` | リモート user の notesCount/followersCount/followingCount を origin の `/api/users/show` から取得 (mk-go 独自拡張、#943)。LRU cache size cap 10000 (#945)、SSRF guard 経由 |
| `note_delivery_hook.go` | ノート公開時にCreate/Announceを配信 |
| `following_delivery_hook.go` | フォロー/アンフォロー/承認時にFollow/Undo/Acceptを配信 |
| `reaction_delivery_hook.go` | リアクション時にLikeを配信 |
| `note_delete_delivery_hook.go` | ノート削除時にDeleteを配信 |
| `mention_resolver.go` | メンション → AS Mentionタグ変換 |
| `fetcher.go` | 署名なしGETによるリモートオブジェクト取得 |

## HTTP Signatures

Cavage draft v12に準拠。RSA-SHA256で署名する。

**署名対象ヘッダー:**
- `(request-target)`: メソッド + パス
- `date`: RFC 2822形式
- `host`: リクエストホスト
- `digest`: リクエストボディのSHA-256 (POSTのみ)

```
Signature: keyId="https://example.com/users/abc#main-key",
           algorithm="rsa-sha256",
           headers="(request-target) date host digest",
           signature="base64..."
```

すべての送信リクエストに署名を付与する。受信時は`keyId`からアクターの公開鍵を取得して検証する。

## リモートオブジェクト解決

`resolver.go`がリモートのアクターとノートを取得し、ローカルDBに永続化する。

**公開鍵キャッシュ (2層):**
1. インメモリ (`map[userID]publicKeyEntry`, TTL 24h)
2. DB (`user_publickey`テーブル)
3. HTTPフェッチ (キャッシュミス時、署名なしGET)

リモートユーザー作成時にはインスタンス情報の登録とチャートメトリクスの記録も行う。

## 配信パイプライン

```
coreサービス (note/following/reaction)
  ↓ フック呼び出し
DeliveryHook (note_delivery_hook等)
  ↓ Activity構築 + エンキュー
DeliverService
  ↓ asynq タスク投入
Redis (ジョブキュー)
  ↓
DeliverProcessor (asynqワーカー)
  ↓ 署名付きPOST
リモートInbox
```

**レスポンス処理:**
- 2xx: 成功
- 410/404: 永続的失敗 (対象が存在しない)、リトライしない
- その他の4xx: 永続的失敗、リトライしない
- 5xx / ネットワークエラー: リトライ (不調状態としてマーク)

### Ed25519署名 (capability-gated, #1067 / #1071)

配送先 actor が FEP-521a Multikey で Ed25519 を expose し、sender が `user_keypair_extra` に Ed25519 鍵を持つ場合のみ Ed25519 で sign を試行する。それ以外は従来通り RSA で sign。

**capability 判定の流れ:**

1. `DeliverToUser(signerUserID, recipient *model.User, body)` で recipient が既知
2. `recipientIsEd25519Capable(recipient)`: `user_publickey_extra` を `ListByUserID(recipient.ID)` で確認 (TTL 5min の in-memory cache あり、N+1 抑制)
3. capable で sender も Ed25519 鍵を持つ → `DeliverPayload.Ed25519KeyID` / `Ed25519PrivPEM` に詰めて enqueue
4. `DeliverProcessor` 側で Ed25519 sign を試行、4xx (410/404 除く) で失敗したら Redis に `ed25519:fail:{host}` を INCR
5. 60s window 内に 3 件以上失敗 → `ed25519:degrade:{host}` を EX=5min で立てる
6. degrade flag 立ち host への次回 deliver は最初から RSA で sign (= broken impl safety net)

`DeliverActivity` / `DeliverToFollowers` 経路 (recipient 不明) は RSA only で動く。

### Redis に格納される秘密情報

DeliverPayload は `KeyPEM` (RSA private key) および `Ed25519PrivPEM` (Ed25519 private key) を含んだ JSON として asynq queue (= Redis) に書き込まれる。これは upstream Misskey TS の BullMQ payload と同じ pattern だが、本番運用では以下が推奨される:

- **Redis 通信の TLS 化** (in-transit 暗号化)
- **Redis の persistence 暗号化** (encrypt-at-rest、Disk full encryption / Redis Enterprise の透過暗号化など)
- queue worker と Redis 間のネットワーク隔離 (private VPC / Unix socket)

特に Ed25519 / RSA いずれも HTTP Signature の identity を担う秘密情報のため、queue server を信頼境界の内側に置くこと。

## Inbox処理

`internal/api/inbox/handler.go`がShared InboxとユーザーInboxの両方を処理する。**verify-in-worker 化済 (#565)**: HTTP handler では body + signature header を payload として queue に詰めて 202 即返し、verify は inbox worker (asynq processor) で行う。これにより HTTP 受信スループットが TS の 2.6-2.8x。

**HTTP handler フロー (同期、軽量):**
1. リクエストボディ読み取り
2. queue/processors の inbox 用 payload を作成 (body + signature header)
3. asynq enqueue
4. 202 Accepted 即返し

**inbox worker フロー (非同期、`internal/queue/processors/inbox.go`):**
1. payload から body / Signature header を復元
2. Signature 解析 → `keyId` からアクター解決
3. 公開鍵取得 (キャッシュ優先) → 署名検証
4. ホストブロックチェック
5. インスタンスメタデータ更新 (`InstanceTouchBuffer` で per-host 1s buffer 集約、#569)
6. チャートメトリクス記録
7. Processor にディスパッチ
8. fanoutHook / notificationHook を `safeGo` で async 発火 (#569)

未対応のActivity種別にも202 Acceptedを返す (エラーにしない)。

## 遅延配送 note の createdAt (#940)

origin instance の downtime / 遅延配送等で過去の note が遅れて inbox に到着した場合、AP Object の `published` field を parse して time-based ID (aidx) に渡すことで timeline 並びを origin と揃える。

- 受け入れ: RFC3339 / RFC3339Nano
- spoof guard: 未来側 `+5min` skew tolerance を超える値は受信時刻にフォールバック
- parse バグ guard: 過去 10 年以上前の値もフォールバック
- 該当 helper: `internal/core/federation/published_time.go` の `parseAPPublishedTime`

## AP variant handling

upstream / 他実装が出してくる variant に対するロバスト性 (一部は #947 配下で対応中):

| variant | 状態 |
|---|---|
| `published` parse + 異常値 fallback | ✅ 対応済 (#940) |
| `attributedTo` / `actor` の string / object 双方受け入れ | 🟡 #947 配下で対応予定 (upstream #17340) |
| `alsoKnownAs` の array / string 双方受け入れ | 🟡 #947 配下で対応予定 (upstream #17275) |
| 存在しない Actor の Delete を ignore | 🟡 #947 配下で対応予定 (upstream #17294) |
| リレー由来 Announce で renote を作らず元 note を直接 publish | 🟡 #947 配下で対応予定 (upstream #17308) |
| ブロック中インスタンスの inbox job 蓄積防止 | 🟡 #947 配下で対応予定 (upstream #17336) |

## RemoteStatsFetcher (mk-go 独自拡張、#943)

upstream Misskey TS の `users/show` は **自インスタンスで観測した範囲** のみで notesCount / followersCount / followingCount を集計するため、リモートユーザーの数値が実体より小さく表示される。mk-go は user.Host が non-local の場合、origin instance の `/api/users/show` を https POST で叩いて公開 counts を取得し、上書き表示する。

- LRU cache: size cap 10000 / positive TTL 1h / negative TTL 5min (#945)
- SSRF guard: `safehttp.NewSSRFSafeTransport` 経由
- host validation: URL injection (`/`, `?`, `#`, `@`, ` `) を url.Parse で reject
- 失敗時は silent fallback で local 観測値を維持
- フォロー一覧 / フォロワー一覧 endpoint は **自インスタンス上に存在する関係のみ** (= 数値だけ remote、一覧は local の非対称設計)

## レンダリング

`renderer.go`がGoモデルをAP JSON-LDに変換する。

**Person:**
- Type: `Person` (ボットは`Service`)
- アイコン/バナー、公開鍵、プロフィールフィールド (PropertyValue)
- カスタム絵文字タグ

**Note:**
- `content`: MFM → HTML変換
- `_misskey_content`: 元のMFM
- メンション、ファイル添付 (Document)、引用URL
- `sensitive`フラグ

## 対応済みActivity

| Activity | 送信 | 受信 |
|---|---|---|
| Create (Note) | o | o |
| Delete (Note) | o | o |
| Update (Note/Person) | o | o |
| Follow | o | o |
| Accept (Follow) | o | o |
| Reject (Follow) | o | o |
| Undo (Follow/Like/Announce/Block) | o | o |
| Like (Reaction) | o | o |
| Announce (Renote) | o | o |
| Block | o | o |
| Flag (Report) | - | o |
| Move (Account Migration) | - | o |
| Add/Remove (Pin) | - | o |

## ディスカバリ

| エンドポイント | 内容 |
|---|---|
| `GET /.well-known/webfinger` | `acct:user@host`のリソース検索 |
| `GET /.well-known/host-meta` | WebFinger URLテンプレート |
| `GET /.well-known/nodeinfo` | NodeInfo URL |
| `GET /nodeinfo/2.0` | サーバーメタデータ (バージョン、プロトコル、利用統計) |
| `GET /users/:id` | AP Person |
| `GET /notes/:id` | AP Note |
| `GET /users/:id/followers` | フォロワーコレクション |
| `GET /users/:id/following` | フォローコレクション |
| `GET /users/:id/featured` | ピン留めノートコレクション |
