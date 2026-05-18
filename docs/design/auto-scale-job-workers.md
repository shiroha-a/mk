# Job queue worker auto-scaling

**Status**: Proposed (#1120 tracker, 2026-05-18) / **Scope**: `internal/queue/autoscale/` (new) + `internal/queue/driver/mkqdriver/` + `internal/server/queue_factory.go`

---

## 1. 背景

### 1.1 operator UX の現状問題

mk-go の job queue は `internal/queue/queue.go` で 5 系統の queue (`deliver` / `inbox` / `export` / `push` / `webhook`) を定義しており、operator は以下の knob を YAML で握る:

```yaml
deliverJobConcurrency: 16      # default
inboxJobConcurrency:   16
deliverJobPerSec:      128
inboxJobPerSec:        128
deliverJobMaxAttempts: 12
inboxJobMaxAttempts:    8
redisForJobQueue.poolSize: <cores * 10>
db.poolSize:            10-100
```

(参考: `relationshipJobConcurrency` は TS-compat の受け付けのみで mk-go では no-op、`queue_factory.go` で forward されない)

これらの「最適値」は workload に強く依存する:

- 連合先の応答速度 (slow remote inbox を多く抱える instance は worker を増やしても drain しない)
- DB の placement (同一 host vs 別 host、後者は RTT +5ms で全 job に影響)
- antenna / webhook の多さ (1 note で fanout が 10x される鯖もある)
- traffic pattern (バースト型の relay flood vs sustained low-volume)

upstream Misskey TS / mk-go 共通の運用実態として、**operator は default 16 のまま放置するか、過去の経験則でやや増やす程度** で、本来は host ごとに optimal を実測すべきところが省略されている。結果として「Misskey は重い」と言われる主要因の一つになっている。

### 1.2 Go の goroutine semantics

加えて mk-go では BullMQ (Node.js) の「worker process 数 = 並列 CPU 数」の感覚で設定すると過少配分になる。Go の goroutine は I/O bound 待ち時間中に他 goroutine へ yield するため、deliver のような I/O dominant な workload では cores × 8-16 程度が optimal となる。

I/O bound thread pool の経験則 (Brian Goetz "Java Concurrency in Practice" §8.2 由来の Erlang 形式 heuristic):

```
optimal ≈ cores × (1 + WaitTime / ServiceTime)
deliver: 8 × (1 + 150ms / 10ms) = 128
inbox:   8 × (1 + 50ms / 50ms)  = 16
```

operator がこの計算を host ごとにやることは現実的でない。

### 1.3 既存の knob では届かない

- `deliverJobConcurrency: N` は **固定値** で動的調整できない。spike 時は不足、idle 時は過剰
- 過剰時の cost: 各 worker goroutine は Redis dispatch loop で接続を 1 本保持 → Concurrency=128 だと idle でも 128 Redis 接続を hold
- spike 時の cost: 既存 default 16 で 5000 deliver burst が来ると drain に ~60s かかる

## 2. 提案

`jobQueueAutoScale: true` を opt-in flag として導入し、**AIMD (additive-increase, multiplicative-decrease) controller** が queue depth / dispatch wait を観測しながら worker 数を `[minWorkers, maxWorkers]` の範囲で自動調整する。

operator が握る knob は **2 個に縮約**:

```yaml
jobQueueAutoScale: true
maxWorkers: <DefaultMaxWorkers>     # 詳細は §3.6
# minWorkers (default 4) / autoScaleCooldownSeconds (default 1) は通常触らない
```

`maxWorkers` のデフォルト計算 (本 ADR 全体で参照する単一定義) :

```
DefaultMaxWorkers = runtime.NumCPU() × 8
```

既存の `deliverJobConcurrency` 等が明示設定されている場合は **個別 knob を尊重** (= controller を無効化)、後方互換を完全に維持する。

## 3. 設計判断

#1120 tracker で挙げた 6 open question への明示回答。

### 3.1 scale signal: AIMD on queue depth (start) → PI on dispatch wait (将来)

**初期実装は AIMD on queue depth** を採用する。

- queue depth が `upThreshold` (= 現 worker 数 × 4) を超えたら additive-increase
- queue depth が `downThreshold` (= 現 worker 数 × 0.5) を下回ったら multiplicative-decrease

理由:
- AIMD は TCP congestion control で 30 年の運用実績がある oscillation-safe algorithm
- dispatch wait p95 を直接見る PI controller は理論上より賢いが tuning が難しく、初期 release で失敗する確率が高い
- queue depth は Redis ZCARD 1 op で取得可能、観測コストが極小

将来 PI controller への差し替えは `Controller` interface 経由で可能に設計する (#1123 で interface 定義)。

### 3.2 scope: per-queue

global controller (driver 1 つで全 queue 統合) ではなく、**queue ごと独立 controller** を採用する。

理由:
- deliver と inbox は workload 性質が全く違う (I/O bound vs CPU-mixed)
- 1 queue (e.g. deliver) の spike が他 queue の worker を奪うのは望ましくない
- per-queue は実装が単純で、global cap は別途 `maxWorkers` で表現できる

global cap は `maxWorkers` を **driver 全体の総 budget** として解釈し、controller は per-queue で `maxWorkers / len(queues)` までスケールする。

### 3.3 scale unit: AI `+max(1, N×0.25)` / MD `N×0.5`

| direction | step | 根拠 |
|---|---|---|
| **scale-up** | `+max(1, N×0.25)` (additive) | TCP-AIMD と同じ、conservative にスケール、4 step で倍増 |
| **scale-down** | `N×0.5` (multiplicative) | 急減で idle resource 解放を速くする、cost-bound design 重視 |

doubling は spike 時に高速だが overshoot で oscillation を起こしやすく、TCP slow-start の経験から AIMD の方が安定。

### 3.4 cool-down: 1 秒

scale event 間の最小 interval は **1 秒**。

- queue は ms スケールで変動するが、1 秒の遅延は drain time 全体 (10s-60s) に対し許容範囲
- Redis ZCARD を毎 100ms で叩く負荷は許容できないため、観測周期 = cool-down = 1s で統一
- ハイステリシス無し (scale up / down 両方とも同じ 1s)

### 3.5 multi-process 協調: 初期 release は no coordination

同一 Redis を共有する複数 mk-go process が独立に auto-scale すると、合計 worker 数が想定を超える (multi-pod 環境):

- pod 1 が maxWorkers=128 までスケール
- pod 2 も独立に maxWorkers=128 までスケール
- → cluster 合計 256 worker、DB pool 飽和

初期 release では **協調なし** とし、operator は **per-process budget を operator が割る前提**で `maxWorkers` を設定する。cluster-wide budget の協調機構 (Redis 上の lease token 等) は将来 issue (#1120 tracker の「非ゴール」セクションに明示)。

multi-pod 運用への現実的アドバイス:
- 「pod 数 × per-pod maxWorkers ≦ DB pool size × 0.8」を operator が確認
- 例: 3 pod、DB pool=100 → per-pod maxWorkers=24 を推奨

### 3.6 既存 knob との優先順位

```
個別 knob (deliverJobConcurrency 等)
    > maxWorkers
        > controller (auto-scale)
```

- `deliverJobConcurrency: N` が明示設定 → controller は当該 queue を **管理対象から外す**、N 固定で動作
- `maxWorkers: M` 設定 → controller の上限として使う
- 両方未設定 → `DefaultMaxWorkers` (§2 で定義) を採用

これにより:
- 既存 operator (固定 knob 使用) は `jobQueueAutoScale: true` を後付けしても影響なし
- 部分的に auto-scale を試したい人 (e.g. deliver だけ controller 管理) は `inboxJobConcurrency: 16` だけ明示すれば inbox は固定、deliver は controller 管理

## 4. アーキテクチャ

```
┌─────────────────────────────────────────────────────────────┐
│                        Server (mk-go)                        │
│                                                              │
│  ┌──────────────┐         ┌─────────────────────────────┐  │
│  │ queue_factory│────────▶│  AIMDController (per queue) │  │
│  │              │         │  ┌────────────────────────┐ │  │
│  │  - reads cfg │         │  │  Observe(metric)       │ │  │
│  │  - builds    │         │  │   → scale-up / down /  │ │  │
│  │    controller│         │  │     no-op decision     │ │  │
│  │  - injects   │         │  └────────────────────────┘ │  │
│  │    into      │         │  Bounds: [min, max] enforced│  │
│  │    driver    │         └─────────────────────────────┘  │
│  └──────────────┘                       │                   │
│         │                               ▼                   │
│         │              ┌────────────────────────────────┐  │
│         └─────────────▶│  mkqdriver.Server              │  │
│                        │  ┌──────────────────────────┐  │  │
│                        │  │  Resize(queue, n) API    │  │  │
│                        │  │  - spin up / down workers│  │  │
│                        │  │  - graceful drain        │  │  │
│                        │  └──────────────────────────┘  │  │
│                        │  ┌──────────────────────────┐  │  │
│                        │  │  WorkerPool (per queue)  │  │  │
│                        │  │  goroutine[1..N]         │  │  │
│                        │  └──────────────────────────┘  │  │
│                        └────────────────────────────────┘  │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │  MetricCollector (Prometheus)                        │   │
│  │  - mk_job_queue_worker_count{queue}                 │   │
│  │  - mk_job_queue_depth{queue}                        │   │
│  │  - mk_job_dispatch_wait_seconds{queue}              │   │
│  │  - mk_job_scale_events_total{queue, direction}      │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 4.1 controller lifecycle

```
Server 起動時:
  if cfg.JobQueueAutoScale:
    for queue in [deliver, inbox, export, push, webhook]:
      if !cfg.<queue>JobConcurrency.IsSet():
        controllers[queue] = NewAIMDController(min, max/len(queues), cooldown)
        go controllers[queue].Run(ctx)  # 1s tick で observe + decide

ticker (per controller, 1s):
  depth = Redis.ZCARD(queue)
  current = driver.WorkerCount(queue)
  action = controller.Observe(depth, current)
  switch action:
    case ScaleUp(n):
      driver.Resize(queue, n)
      metrics.IncScaleEvents(queue, "up")
    case ScaleDown(n):
      driver.Resize(queue, n)
      metrics.IncScaleEvents(queue, "down")
    case NoOp:
      ;

Server 停止時:
  cancel(ctx)  # controller goroutine 終了
  driver.Close()  # worker graceful drain
```

### 4.2 cost-bounded design

連合 flood / retry storm / runaway webhook 等で controller が cluster を喰い尽くす事故を防ぐため、以下の **多層防御**:

1. **`maxWorkers` hard cap** — controller は cap を超えてスケールしない
2. **enqueue 側 backpressure** (将来 issue) — queue depth > 閾値時に inbox HTTP が 503 Retry-After で送信側に押し戻す
3. **host-level circuit breaker** (既存 #1067 系の拡張、将来 issue) — 落ちてる相手への retry が cluster を食わない
4. **per-queue scope** — 1 queue spike が他 queue の budget を奪わない
5. **panic switch** — `jobQueueAutoScale: false` 一発で controller off、固定値運用に戻せる

## 5. multi-driver 整合 (mkq / asynq)

### 5.1 mkqdriver

#### 制約: mkq library 側に Resize API は無い

`github.com/shiroha-a/mkq@v1.0.1` の `worker_options.go` では `concurrency int` が `mkq.NewWorker` 構築時に固定される設計で、起動後の動的変更 API は存在しない。本 ADR では mkq library 自体には変更を加えず、**mkqdriver layer に Worker 群を管理する pool-of-workers 層を追加** する方針を採る (mkq library への PR は別途中長期検討、本 tracker scope 外)。

#### 実装: pool-of-Workers 方式

`mkqdriver.Server` 内に **per-queue で複数の mkq.Worker を保持する pool 層** を追加 (#1124):

```
mkqdriver.Server
 ├─ workerPools map[queue] *WorkerPool
 │   └─ WorkerPool
 │       ├─ workers []*mkq.Worker  (各 Worker は WithConcurrency(1) 固定で起動)
 │       ├─ activeCount int        (= 起動済 Worker 数 = 仮想的な総 concurrency)
 │       └─ mu sync.Mutex
 │
 └─ Resize(queue, n) error:
     - n > activeCount: 不足分の mkq.Worker を新規起動 (WithConcurrency(1))
     - n < activeCount: 余剰 Worker に Close を呼び、in-flight job 完了を待って join
     - n == activeCount: no-op
```

各 Worker を `WithConcurrency(1)` で起動して個別 Worker 単位で start / stop することで、library 側の API を変えずに **driver layer から動的 scale を実現** する (細粒度 control 重視、library への侵襲ゼロ)。

`Server.Close` は workerPools 全 Worker の Close を sync.WaitGroup で待つ既存挙動と整合する (現状の close 経路を WorkerPool 単位に差し替えるだけ)。

#### trade-off

- ✓ mkq library 変更不要、upstream とすぐ整合
- ✓ scale-down の granularity が 1 worker 単位、stop も Worker 単位で graceful
- ✗ N=128 のとき mkq.Worker が 128 個動く = WithConcurrency(128) の 1 Worker より overhead 微増 (内部 dispatch loop が 128 本動く)
- ✗ overhead 評価は #1124 の integration test で測定、許容できない場合は mkq library に Resize API を追加する PR を別途検討

### 5.2 asynqdriver

asynq library は `Concurrency` を Server 構築時に固定する設計で、動的 Resize に対応する API がない (upstream に PR を出す or fork が必要)。

初期 release では **asynqdriver は auto-scale 対象外** とし、`jobQueueAutoScale: true` + `jobQueueDriver: asynq` の組み合わせは config 検証で reject (or warning + 固定値 fallback)。

将来 asynq に Resize 相当の API が入った時点で対応する (`#1120 tracker` の「将来」項目)。

## 6. observability

### 6.1 Prometheus metric (#1122 で先行 export)

| metric name | type | labels | 説明 |
|---|---|---|---|
| `mk_job_queue_worker_count` | gauge | queue | 各 queue の active worker goroutine 数 |
| `mk_job_queue_depth` | gauge | queue | Redis ZCARD 値 (pending job 数) |
| `mk_job_dispatch_wait_seconds` | histogram | queue | enqueue → dispatch までの待ち時間 |
| `mk_job_processing_seconds` | histogram | queue, status | job 処理時間 (success / failure) |
| `mk_job_scale_events_total` | counter | queue, direction | auto-scale 起動回数 (up / down) |

### 6.2 logging

scale event 毎に slog で 1 行記録:

```
slog.Info("autoscale: scaled",
  "queue", "deliver",
  "direction", "up",
  "from", 16,
  "to", 24,
  "depth", 312,
  "wait_p95_ms", 480)
```

operator が「いつ何が起きたか」を grep で追えること。

### 6.3 docs/configuration.md 更新

- 新 config の説明
- 既存 knob との優先順位
- multi-pod 運用での `maxWorkers` 推奨値計算式
- panic switch (auto-scale off) 手順

## 7. tests strategy

### 7.1 controller unit test (#1123)

`internal/queue/autoscale/aimd_test.go` で AIMDController の state machine を table-driven test で全 transition を網羅:

- scale-up trigger (depth > upThreshold)
- scale-down trigger (depth < downThreshold)
- cool-down 中の no-op
- max bound 到達時の cap
- min bound 到達時の floor
- time injection (fake time での deterministic test)

実 Redis 不要、pure logic test。

### 7.2 driver integration test (#1124)

`internal/queue/driver/mkqdriver/integration_test.go` に追加:

- `TestServer_ResizeUp_ProcessesMoreInParallel` — Resize(8 → 16) で並列度が倍になることを確認
- `TestServer_ResizeDown_DrainsGracefully` — Resize(16 → 4) で in-flight job が完了してから worker 終了
- `TestServer_ResizeRace` — 同時 multiple Resize 呼び出しで panic / leak しない

testcontainers-go で実 Redis 起動。

### 7.3 e2e bench (#1126)

`tests/queue-bench/` に新シナリオ:

- 固定 N=16 / 固定 N=64 / autoScale enabled の 3-way
- deliver burst 5000 / inbox burst 3000 の drain time 比較
- idle 時の Redis 接続数比較

## 8. phased rollout

#1120 tracker の sub-issue 順序:

| # | PR | 単独 merge | 配線 trigger |
|---|---|---|---|
| 1 | #1122 metric export | ✓ | 常時 export、controller 未稼働 |
| 2 | #1123 AIMD controller library | ✓ | library のみ、配線なし |
| 3 | #1124 mkqdriver Resize | ✓ | Resize API 追加のみ、auto 起動なし |
| 4 | #1125 queue_factory wiring | ✓ | `jobQueueAutoScale: true` 時のみ起動、default false |
| 5 | #1126 queue-bench report | data only | merge gate ではなく documentation |

各 PR は revert 可能、controller の挙動に問題が出ても #1125 を revert すれば固定運用に完全復帰する。

## 9. migration ガイド

### 9.1 既存 operator (auto-scale 不要)

何もしなくて良い。`jobQueueAutoScale` は default false なので、既存設定はそのまま動作する。

### 9.2 auto-scale を試す

1. config に `jobQueueAutoScale: true` を追加 (他は何も変えない)
2. 既存の `deliverJobConcurrency` 等は **削除する** (controller に管理させる、残すと固定値が優先)
3. `maxWorkers: <値>` を設定 (default: `runtime.NumCPU() × 8`、明示推奨)
4. mk-go を再起動
5. grafana / prometheus で `mk_job_queue_worker_count` が動的に変化することを確認
6. `mk_job_scale_events_total` で scale 発火頻度を観測、必要なら `maxWorkers` を調整

### 9.3 panic switch (障害時)

```yaml
jobQueueAutoScale: false  # この 1 行で固定値 fallback
deliverJobConcurrency: 64 # 固定値を明示
inboxJobConcurrency:   32
```

再起動で固定運用に戻る。controller goroutine も終了するため leak しない。

## 10. open issues / 将来 work

#1120 tracker の「非ゴール」セクションに記載済の項目:

- multi-process 協調 scaling (cluster-wide budget、Redis 上の lease token 機構)
- HPA / VPA との連携 (custom metric expose は本 ADR でカバー、k8s 側設定例の docs 化)
- asynqdriver 対応 (upstream に Resize 相当の PR が入った時点で)
- PI controller (dispatch wait p95 を直接 targeting する高度化)
- enqueue 側 backpressure (inbox HTTP の 503 Retry-After)
- host-level circuit breaker の deliver 全般への拡張

## 11. 関連

- Tracker: #1120
- sub-issues: #1121 (本 ADR) / #1122 / #1123 / #1124 / #1125 / #1126
- 既存 mkq design: [mkq-design.md](mkq-design.md)
- 既存 inbox worker design: [inbox-verify-in-worker.md](inbox-verify-in-worker.md)
- queue-bench infra: [../queue-bench.md](../queue-bench.md)
- 参考: TCP congestion control (AIMD) / Kubernetes HPA v2 algorithm / asynq RateLimiter (rate-based throttling との対比)
