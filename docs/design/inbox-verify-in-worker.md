# inbox handler の verify-in-worker 化

**Status**: Active (#565 で実装 2026-05-01) / **Scope**: `internal/api/inbox/` + `internal/queue/processors/inbox.go`

---

## 1. 背景

ActivityPub Inbox (Shared Inbox / per-user Inbox) は外部から大量の signed activity を受け取る。**HTTP Signature 検証** + **ホストブロック判定** + **インスタンスメタデータ更新** + **チャート記録** + **Activity ディスパッチ** をすべて HTTP handler 内で同期実行すると、attacker が unsigned / malformed activity を多量に送ってきたとき HTTP 受信 throughput が下がる。

upstream Misskey TS は `InboxProcessorService` (BullMQ worker) で verify する設計だが、HTTP handler との分担境界が曖昧で、結果として HTTP handler 側でも一部の検証を行っている。

## 2. 設計判断: HTTP handler を 202 即返し、検証は worker

mk-go は **HTTP handler を最小化** し、検証を inbox worker で行う構成にした。

```
[外部 instance]
      │ POST /inbox (signed)
      ▼
[HTTP handler] ───────► body + signature header を payload に詰めて inbox queue へ
      │ 202 Accepted 即返し
      ▼
[ジョブキュー]
      │
      ▼
[inbox worker] ─────► signature verify
                     ├─ ホストブロック判定
                     ├─ インスタンス touch (per-host 1s buffer)
                     ├─ チャート記録
                     └─ Processor ディスパッチ (Create/Update/Follow/...)
                          │
                          ├─ fanoutHook (timeline) — safeGo で async
                          └─ notificationHook (notification) — safeGo で async
```

### HTTP handler が行うこと

1. body を読み取る (上限あり)
2. `Signature` header と関連 header (`Date`, `Host`, `Digest`) を payload と一緒に dump
3. inbox queue へ enqueue
4. 202 Accepted

### inbox worker (`internal/queue/processors/inbox.go`) が行うこと

1. payload から body / Signature header を復元
2. Signature 解析 → `keyId` からアクター解決
3. 公開鍵取得 (キャッシュ優先) → 署名検証
4. ホストブロックチェック
5. インスタンスメタデータ更新 (`coreinstance.TouchBuffer` で per-host 1s buffer 集約、#569)
6. チャートメトリクス記録
7. Processor ディスパッチ (Create / Update / Follow / Like / Announce / etc.)
8. `fanoutHook` / `notificationHook` を `safeGo` で async 発火

## 3. メリット

### HTTP 受信 throughput が向上

queue-bench (`tests/queue-bench/`、#563) で計測:

| metric | 旧 (verify in handler) | 新 (verify in worker) |
|---|---|---|
| mk-go inbound HTTP rps | 684/685 | **2812/3017** (4.1-4.4x) |
| TS BullMQ inbound HTTP rps | 1064 | (変わらず) |
| 比較 | mk-go < TS | **mk-go = TS の 2.6-2.8x** |

これにより mk-go 全 endpoint が TS 同等以上を達成。

### worker drain time も改善 (#569)

- `MarkRequestReceived` を per-host で 1s buffer に集約する `coreinstance.TouchBuffer` 導入
- federation processor の `handleCreate` で Reply/Renote 関係が無い fresh note への redundant `hydrateNoteForFanout` (DB SELECT) を skip
- fanoutHook / notificationHook を local note service と同じ `safeGo` pattern で async 化

queue-bench で **asynq drain 29.3s → 22.4s (-24%)、mkq 45.7s → 34.0s (-26%)**。

## 4. Trade-off

### Security

- unsigned / malformed activity は worker で drop されるため queue が一時的に膨らむ可能性あり
- 攻撃想定: 攻撃者が 202 即返しの handler に大量の偽 activity を投げ込み、worker queue を埋める
- 対策: CDN / WAF 層で粗い filter を入れる前提 (= mk-go 単独で粗 filter は持たない、Misskey TS 同様)
- 検証 fail した activity の queue 上での生存期間は driver の retention policy 依存

### 可観測性の劣化

旧構成では HTTP handler のレスポンスコードで「sig verify ok / fail」を即時に分かったが、新構成では worker ログを見ないと分からない。
→ inbox worker 側で fail metric を Prometheus に出すか、構造化ログで `inbox.verify.failed` を出して観測可能性を保つ。現状は slog の Warn level で残している (= UDS production で aggregator から見える)。

### 順序保証

旧構成では handler ↔ Processor 間で Activity の順序が保たれていた (= 同 actor の Create → Update が順番に処理される保証)。新構成ではジョブキュー経由なので、worker concurrency > 1 にすると順序がずれる可能性がある。

→ Misskey TS / Mastodon でも同 worker concurrency 環境で順序保証は提供されない。Activity 自体に `published` 時刻があるので最終的整合は取れる (= 順序ずれは UI の即時性のみに影響)。

## 5. 関連

- PR: #565 (verify-in-worker 化), #569 (worker drain time 短縮)
- queue-bench 基盤: #563、`docs/queue-bench.md`
- 関連 doc: [docs/federation.md](../federation.md) の「Inbox 処理」section、[docs/architecture.md](../architecture.md) の `internal/queue/`
