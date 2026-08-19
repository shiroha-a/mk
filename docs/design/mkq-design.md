# mkq: BullMQ互換 Go-native Job Queue ライブラリ 設計ドキュメント

**Status**: 実装済 (#377 Phase 2)。mkq は別 OSS リポジトリとして公開され、**mk-go の
既定 driver** になっている (#571)。**以下は設計時点の記録**で、現在の設定値・挙動の
一次情報ではない。運用時の値は [configuration.md](../configuration.md)、実測は
[queue-bench.md](../queue-bench.md) を参照。

---

## 1. 背景

設計当時、mk-go は [asynq](https://github.com/hibiken/asynq) をジョブキューとして使用していた。しかし本家 Misskey (TS) は [BullMQ](https://docs.bullmq.io/) を採用しており、admin UI (ジョブキュー画面) は BullMQ 前提で以下の情報を要求する:

| BullMQ (frontend hardcode) | asynq (mk-go runtime) |
|----------------------------|-----------------------|
| Queue: `deliver` / `inbox` / `db` / `userWebhookDeliver` / `system` / `objectStorage` / `relationship` / `postScheduledNote` / `endedPollNotification` | `deliver` / `push` / `webhook` / `export` / `maintenance` |
| State: `waiting`/`active`/`completed`/`failed`/`delayed`/`paused`/`repeat` | `pending`/`active`/`scheduled`/`retry`/`aggregating`/`completed`/`archived` |
| Per-job: `progress` / `returnValue` / `stacktrace[]` / `opts.repeat` | — |
| Per-queue: Redis `memory.peak` / `uptime` / `clients.connected` / pause 制御 | — |

現行 #344 PR では frontend 崩壊を避けるため「未対応 field を 0 埋め」しているが、**意味論的には嘘**(存在しない queue を「0 jobs」で表示する等)。

本ドキュメントは #377 の解決策として、**BullMQ 互換の wire protocol を持ちつつ、Go のジェネリクス / goroutine / context を素直に活かす新規ジョブキューライブラリ `mkq`** の設計をまとめる。独立 OSS として開発し、mk-go から driver 経由で asynq と切り替え可能にする。

## 2. Goals / Non-Goals

### Goals

- **BullMQ と互換な Redis data layout** を保つ。bull-board / Misskey admin UI がそのまま動く
- **Go-native 型安全 API**: generics で payload 型を Queue に紐付け
- **goroutine ベース並行処理**: Node.js の worker thread モデルを Go の goroutine に置換して効率化
- **Observability**: OpenTelemetry + Prometheus + slog を標準装備
- **Scheduler / repeat / retention / progress / stacktrace** を native サポート
- **他言語 worker 互換**: BullMQ Redis layout 準拠なので、既存の Node.js / Python BullMQ worker と同じキューを共有可能

### Non-Goals

- BullMQ の JavaScript API を 1:1 再現すること (Go らしい API 優先)
- sandbox process / IPC (asynq も Node.js BullMQ も外したもの。Goroutine で十分)
- 100% Lua script 互換性 (必要最小限の Lua のみ使用、残りは Go で実装)
- プラグインシステム (shipped observability adaptor で足りるはず)

## 3. 名称 / 配置

候補:

- `github.com/shiroha-a/mkq` ← 採用予定 (mk 本体と naming 一貫、短い)
- `github.com/shiroha-a/bulldog` ← BullMQ 連想だが Go らしさ弱い

別リポジトリとして公開、mk-go は go.mod で depends。

## 4. API 設計

### 4.1 Queue 定義

```go
// 呼び出し側 (mk-go) で型を定義する。payload は JSON serialize 可能であること。
type DeliverPayload struct {
    Inbox string `json:"inbox"`
    Body  []byte `json:"body"`
}

var DeliverQueue = mkq.Define[DeliverPayload]("deliver",
    mkq.WithConcurrency(16),
    mkq.WithRateLimit(300, time.Second), // 300 jobs/s max
    mkq.WithRetention(mkq.KeepCompleted(100), mkq.KeepFailed(200)),
)
```

`mkq.Define` は `*Queue[T]` を返す。後述の `Add` / `Process` が型安全。

### 4.2 ジョブ投入

```go
err := DeliverQueue.Add(ctx, DeliverPayload{Inbox: "...", Body: body},
    mkq.WithDelay(5*time.Second),
    mkq.WithAttempts(8),
    mkq.WithBackoff(mkq.ExpBackoff(1*time.Second, 30*time.Second)),
)
```

### 4.3 Worker 登録

```go
mkq.Process(DeliverQueue, func(ctx context.Context, job *mkq.Job[DeliverPayload], p mkq.Progress) (any, error) {
    // progress 報告 (UI 側でリアルタイム表示)
    p.Set(10)
    if err := deliver(ctx, job.Data); err != nil {
        // panic → runtime.Stack 自動取得して stacktrace[] に入る
        return nil, err
    }
    p.Set(100)
    return map[string]any{"ok": true}, nil // returnValue
})
```

ハンドラ内で panic すると mkq が recover → stack trace を job.stacktrace に保存 (BullMQ 互換)。

### 4.4 Scheduler / repeat

```go
mkq.Schedule(DeliverQueue, DeliverPayload{...},
    mkq.WithCron("0 0 * * *"),         // 毎日 00:00 UTC
    mkq.WithJobID("daily-digest"),     // unique ID で重複 enqueue 抑止
)
```

BullMQ の repeat option (`every: ms` / `pattern: cron` / `limit` / `endDate`) を `mkq.With...` に 1:1 対応。

### 4.5 Pause / Resume / Drain

```go
DeliverQueue.Pause(ctx)   // 新規 dequeue 停止、既存 active は走り切る
DeliverQueue.Resume(ctx)
DeliverQueue.Drain(ctx)   // 全 waiting job を削除
DeliverQueue.Obliterate(ctx) // queue ごと削除 (非推奨、test用途)
```

### 4.6 Observability

```go
mkq.Init(mkq.Config{
    Redis:  redisOpts,
    Prefix: "host:",                          // BullMQ の keyPrefix 互換
    Tracer: otel.Tracer("mkq"),               // optional
    Meter:  otel.Meter("mkq"),                // optional
    Logger: slog.Default(),                   // default logger
})
```

標準で以下のメトリクスを export:

- `mkq_job_processed_total{queue,status}` counter
- `mkq_job_duration_seconds{queue}` histogram
- `mkq_queue_size{queue,state}` gauge
- `mkq_active_workers{queue}` gauge

## 5. Redis Storage Layout

BullMQ と wire compatible。既存の bull-board / Misskey admin UI / 他言語 worker と同じキューを共有できる。

### 5.1 Key naming

```
{prefix}:{queue}:id           INCR でjob id発行
{prefix}:{queue}:wait          LIST (FIFO順のjob id)  ← 通常のwait queue
{prefix}:{queue}:prioritized   ZSET (score=priority*2^40+age、BullMQ v5+の優先度付きwait)
{prefix}:{queue}:active        LIST (currently processing)
{prefix}:{queue}:delayed       ZSET (score=unix epoch ms)
{prefix}:{queue}:completed     ZSET (score=finishedOn, LRU via ZREMRANGEBYRANK)
{prefix}:{queue}:failed        ZSET (score=finishedOn)
{prefix}:{queue}:stalled       SET (stalled detection用のjob id集合)
{prefix}:{queue}:meta          HASH (paused flag / limiter / opts等のqueue-global state)
{prefix}:{queue}:repeat        ZSET (repeat jobs)
{prefix}:{queue}:repeat:{hash}:{ts}  Job (repeatインスタンス)
{prefix}:{queue}:{jobId}       HASH (data/opts/progress/returnvalue/stacktrace/...)
{prefix}:{queue}:{jobId}:logs  LIST (job単位のlogline)
{prefix}:{queue}:{jobId}:lock  STRING (lock token, TTL付き。失効でstalled判定)
{prefix}:{queue}:events        Redis Stream (BullMQ QueueEvents互換のpubsub)
{prefix}:{queue}:limiter       HASH (rate limiterのtoken bucket state)
```

**BullMQ v5+準拠のポイント**:

- `wait` はpriorityなしjobのFIFO LIST。priority付きjobは `prioritized` ZSET に入る。Worker dequeue時は priority を先に、次に wait を見る。(旧spec で「waitがZSET」と記述したのは誤り)
- **pause は `meta.paused` flag**: 別のpaused keyに退避するのではなく、meta HASHのフラグ立てで実現。worker dequeue時にflagをcheckしてblock。移動コスト無しで pause/resume が即反映 (BullMQ v5+ 実装)
- **Stalled detection は SET + lock token**: 各jobは `{jobId}:lock` に TTL 付きlock tokenを書く。worker が heartbeat (lock renewal) を怠るとTTL 失効→stalled判定→`stalled` SET 経由で wait に差し戻し+attempts++

### 5.2 Lua scripts

BullMQ は 30+ の Lua scripts を使う。mkq も「条件分岐 + 書き戻し」が atomic でないと破綻する操作は Lua で書く必要がある (MULTI/EXEC は all-or-nothing だが intermediate result で branch できないので rate limiter / stalled / pause には不十分)。

初期ターゲットの **8 本の Lua script**:

1. `addJob`: id発行 + Job HASH 保存 + wait LIST or prioritized ZSET or delayed ZSET のいずれかにenqueue (priority/delay判定はここで branch)
2. `moveToActive`: (prioritized ZPOPMIN or wait RPOP) → active + lock token 発行 + stalled SET 登録。meta.paused が真なら no-op で early return
3. `moveToFinished`: active → completed/failed + stalled SET 削除 + lock削除 + retention (ZREMRANGEBYRANK) + returnvalue/stacktrace 書き込み
4. `retryJob`: failed → delayed に retry backoff 込みで再 enqueue (attempts++ も atomic)
5. `promoteJob`: delayed → wait/prioritized への移動 (scheduler tick)
6. `renewLock`: worker の heartbeat。lock TTL を延長 + active に留まっていることを確認
7. `rateLimiterCheck`: token bucket 判定 + 減算を atomic に
8. `moveStalledToWait`: lockが失効したactive jobをwait/prioritizedに戻す + attempts++。lockTTLを超えたjobを stalled SET に登録しつつfail相当の処理をする

残りの「単純な read / write」は Go 側で `MULTI/EXEC` or `WATCH/CAS`。例: job status 照会、queue size 集計、admin API の list 系。

Lua は Redis server で parse cache されるので、startup 後は SHA 呼び出しのみ。client 側はロード処理を initialization 時に1回やるだけ。

### 5.3 Protocol 互換性

- **JSON shape**: Job の `data` / `opts` / `stacktrace[]` / `progress` / `returnvalue` の field 名を BullMQ に準拠
- **ID generator**: BullMQ と同じ INCR counter
- **Key separator**: BullMQ と同じ `:` 区切り
- **TTL**: completed/failed の ZSET は `ZREMRANGEBYRANK` で LRU 保持。`keepLast` が設定されていれば worker 側で自動 trim

他言語の BullMQ client / bull-board から `mkq` が管理するキューを読み書きできる。

## 6. BullMQ 互換戦略

### 6.1 何を互換にするか

- **Redis data layout**: ◎ 完全互換
- **JSON payload shape**: ◎ Job record 完全互換
- **Lua script signature**: △ moveToActive / moveToFinished は同等 semantics、script sha は独自

### 6.2 何を互換にしないか

- **JS client API**: ✗ Go らしい API を優先
- **sandbox process**: ✗ goroutine で代替
- **worker scaling**: ✗ Node.js の cluster 相当は不要 (goroutine で十分)

### 6.3 admin UI (frontend) 表示

Misskey frontend は `admin/queue/list` 等で BullMQ shape を要求するので、mk-go 側に **薄い変換 handler** を用意:

```go
// mk-go: internal/api/admin/queue_bull_adapter.go (既存もある)
func adaptMkqStateToBullJSON(job *mkq.Job) bullShape {
    // progress / returnvalue / stacktrace / opts.repeat をそのまま pack
}
```

mkq のネイティブ state が BullMQ と同じ命名なので変換はほぼ no-op。

## 7. mk-go 統合アーキテクチャ

### 7.1 Driver 抽象化 (Phase 1 で先行実装)

```go
// internal/queue/driver.go
type Driver interface {
    Client() queue.ClientAPI      // Enqueue / EnqueueUnique
    Server() queue.ServerAPI      // Handle / Start / Shutdown
    Inspector() queue.InspectorAPI // for admin queue dashboard
    Scheduler() queue.SchedulerAPI
}

// 現行 asynq 実装
type AsynqDriver struct { /* ... */ }

// 将来 mkq 実装
type MkqDriver struct { /* ... */ }
```

### 7.2 Config で runtime 切替

```yaml
# .config/default.yml
jobQueueDriver: mkq    # 既定 (未指定でも mkq)
# jobQueueDriver: asynq  # legacy
```

**設計当時は asynq が既定だった。** 3-way ベンチ (BullMQ / asynq / mkq、#563) の結果を
受けた #571 の audit で mkq が既定になり、asynq は legacy 扱いになっている。

router.go で driver を 1 箇所で生成して wire。同じ binary で両 driver がコンパイル済み。

### 7.3 Migration path

Section 8 の Phase 番号と一致。Migration path 側は mk-go 視点で圧縮した
view (Section 8 の各 Phase を mk-go 側で見た変化に絞ったもの):

- **Phase 1**: driver 抽象化、既存 asynq は `AsynqDriver` へ移行 (Section 8 Phase 1)
- **Phase 3**: mkq library alpha 開発 (別リポ、mk-go には触らない) (Section 8 Phase 3)
- **Phase 5**: `MkqDriver` を mk-go に追加、CI で両方テスト (Section 8 Phase 5)
- **Phase 6**: `mkq` を default に、`asynq` driver は legacy として残す (Section 8 Phase 6)
- **Phase 7 (数ヶ月後)**: `asynq` driver を deprecate & 削除 (Section 8 Phase 7)

(Section 8 の Phase 2 (本 doc) / Phase 4 (mkq β) は mk-go 側に変化
無しのため Migration path view では省略)

## 8. Phase / Roadmap

| Phase | スコープ | 対応 PR / Issue |
|-------|--------|---------------|
| **1** | mk-go 内 driver 抽象化 (既存 asynq wrapper を interface 化) | mk-go 1-2 PR |
| **2 (current)** | 本設計 doc 合意、mkq リポジトリ作成 | 本 PR |
| **3** | mkq α: Queue / Worker / Scheduler / Retention 基本機能 | mkq 新規 (数 PR) |
| **4** | mkq β: BullMQ 互換 admin API 変換層 + bull-board compat | mkq 追加 PR |
| **5** | mk-go 側 MkqDriver 実装 + CI matrix (asynq / mkq 両方) | mk-go 1-2 PR |
| **6** | production ready: mkq 1.0 release + mk-go default 切替 | OSS 公開 |
| **7** | asynq driver deprecate & 削除 | mk-go 1 PR |

実装見積もり: **Phase 3-4 で合計 3-4 週間** (full-time 換算)、実作業期間は数ヶ月の並行作業。

## 9. Open Questions

- **Stalled detection interval**: BullMQ default は 30s。mkq も同じで OK?
- **Rate limiter semantics**: sliding window vs leaky bucket。Redis script で実装するか、Go 側で実装するか
- **Priority queue support**: BullMQ v5+ の `prioritized` ZSET (score=priority*2^40+age) を採用する方針。spec 先行採用、実装フェーズで performance 検証 (Section 5.1 参照)
- **Sharding**: 大規模 instance (数百 worker) で 1 Redis がボトルネックになったら horizontal shard が要るが、Phase 1-5 では out of scope
- **Persistence**: Redis AOF 前提で良いか、S3 backup 等の仕組みが要るか → mk-go 側の責任として out of scope

## 10. 関連

- 発見元: #344 (asynq → BullMQ 差異でフロント崩壊)
- 前身 issue: #377 (現 issue)
- 参考実装:
  - BullMQ: https://github.com/taskforcesh/bullmq
  - asynq: https://github.com/hibiken/asynq
  - bull-board: https://github.com/felixmosh/bull-board
- 参考: Misskey TS の QueueService / QueueProcessor (packages/backend/src/queue/)

## 11. 決定ポイント (この PR で合意したいこと)

- [ ] リポジトリ名: `github.com/shiroha-a/mkq` で確定
- [ ] Redis layout: BullMQ 互換 (Section 5) で確定
- [ ] API shape: generics ベース (Section 4) で確定
- [ ] Migration Phase: Section 7-8 で確定
- [ ] 本 doc を `docs/design/mkq-design.md` として合意、今後の変更は PR で doc 更新

合意後、Phase 1 (mk-go 内 driver 抽象化) を別 issue で立てて着手、Phase 3 以降は別リポジトリで段階実装する。
