# Job queue worker auto-scaling

**Status**: 実装済 (#1120)。`jobQueueAutoScale: true` で有効になる opt-in 機能として
配線済み / **Scope**: `internal/queue/autoscale/` + `internal/queue/driver/mkqdriver/` +
`internal/server/autoscale_wiring.go`

---

## 1. 背景

### 1.1 operator UX の現状問題

mk-go の job queue は **8 系統**ある (`deliver` / `inbox` / `relationship` /
`export` / `push` / `webhook` / `objectStorage` / `maintenance`)。名前定数は
`internal/queue/queue.go` に 7 つ、cron 専用の `maintenance` だけ
`internal/queue/scheduler.go` にある。

**auto-scale が管理するのはこのうち 7 で、`maintenance` は対象外**
(`autoScaledQueues()`)。以下 §3 の「7 queue」はすべて auto-scale 対象の数。

operator は以下の knob を YAML で握る:

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

(`relationshipJobConcurrency` は #2403 で mkq driver に forward されるようになった。
auto-scale では他の per-queue knob と同じく「設定されていれば `relationship` を
管理対象から外す」判定に使う。asynq driver では依然 no-op で、起動時に warning が出る)

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

operator が握る knob は **2 個 (+ optional 1 個) に縮約**:

```yaml
jobQueueAutoScale: true
maxWorkers: <DefaultMaxWorkers>           # per-queue 上限 (詳細は §3.6)
# minWorkers (default 4) / autoScaleCooldownSeconds (default 1) は通常触らない
# maxWorkersGlobal: 256                   # optional: 全 queue 合計の hard cap (multi-pod 環境用)
```

`maxWorkers` の semantics と default 計算 (本 ADR 全体で参照する単一定義):

```
DefaultMaxWorkers = runtime.NumCPU() × 16        # per-queue 上限
                                                  # 8-core で 128 per queue (deliver 想定 sizing)
```

**`maxWorkers` は per-queue の上限**として解釈する (= "deliver 1 queue が単独でこの数まで膨張できる")。global cap は `maxWorkersGlobal` (optional) で表現し、default は無制限 (= per-queue cap × auto-scale 対象 queue 数 まで膨張し得る)。

multi-pod 等で cluster 全体の DB/Redis pool を守りたい operator のみ `maxWorkersGlobal` を明示設定する (§3.5 で詳述)。

既存の `deliverJobConcurrency` 等が明示設定されている場合は **個別 knob を尊重** (= controller を無効化)、後方互換を完全に維持する。

## 3. 設計判断

#1120 tracker で挙げた 6 open question への明示回答。

### 3.1 scale signal: AIMD on queue depth (start) → PI on dispatch wait (将来)

**初期実装は AIMD on queue depth** を採用する。

scale 判定の trigger:

| direction | 条件 | 補足 |
|---|---|---|
| floor 復帰 | 現 worker 数 < `minWorkers` | depth を見ずに即 `minWorkers` へ。他の 2 つより先に判定する |
| scale-up | queue depth > 現 worker 数 × 4 | 1 観測で即発火 (spike 対応優先) |
| scale-down | queue depth == 0 が **5 cycle (= 5 秒)** 連続 | sustained idle 必須、transient な処理追いつきでは発火しない |

floor 復帰を先頭に置くのは、**0 が吸収状態になるのを防ぐため** (#2657)。scale-up の
判定は `現 worker 数 > 0` を前提にしており閾値も `現 worker 数 × 4` なので、0 だと
分岐に入れない。queue depth != 0 なので sustained-idle 側も素通りし、永久に NoOp を
返し続ける。mkq driver は handler から戻らない worker を生存数から外すので (§5.1.1)、
0 は実際に起こりうる。`minWorkers: 0` (= 明示的に worker を置かない) では発火しない。

scale-down に hysteresis を入れる理由: AIMD 文脈で「TCP packet loss」に相当する明確な signal が queue には無い。`queue depth < N×0.5` 等の閾値だと、worker が一瞬追いついた瞬間 (job 1 件処理完了直後) に発火して oscillation を起こしやすい。**sustained-idle** (= 5 cycle 連続で空) のみ発火に絞れば、worker が真に過剰なときだけ縮める。

理由 (AIMD 採用):
- AIMD は TCP congestion control で 30 年の運用実績がある oscillation-safe algorithm
- dispatch wait p95 を直接見る PI controller は理論上より賢いが tuning が難しく、初期 release で失敗する確率が高い
- queue depth は Redis ZCARD 1 op で取得可能、観測コストが極小

将来 PI controller への差し替えは `Controller` interface 経由で可能に設計する (#1123 で interface 定義)。

**注**: upThreshold 倍率 4 / sustained-idle cycle 数 5 は **#1123 controller unit test + #1126 queue-bench で実測 tuning する暫定値** であり、初期 release 後に operator feedback で調整する可能性あり。

### 3.2 scope: per-queue

global controller (driver 1 つで全 queue 統合) ではなく、**queue ごと独立 controller** を採用する。

理由:
- deliver と inbox は workload 性質が全く違う (I/O bound vs CPU-mixed)
- 1 queue (e.g. deliver) の spike が他 queue の worker を奪うのは望ましくない
- per-queue は実装が単純

各 queue の controller は `maxWorkers` (per-queue 上限、§2 で定義) までスケールする。`maxWorkersGlobal` (optional) が設定されている場合、全 queue worker 合計がこの値を超えるスケール要求は controller 側で reject する (= maxWorkersGlobal 達した時点でそれ以上スケールできない)。

`maxWorkersGlobal` 未設定時は **per-queue 上限の総和まで** (= 7 queue × `maxWorkers=128` = roster 896 worker まで膨張可能。実際に走る worker はこれに隔離ぶんが乗る、§5.1.1。実際は spike が deliver に集中するため deliver 単独で 128 まで scale up、他 queue は概ね `minWorkers=4` 付近の floor で待機)。multi-pod 環境で cluster 全体の DB/Redis pool を守りたい operator のみ明示設定する。

### 3.3 scale unit: AI `+max(1, N×0.25)` / MD `N×0.5`

| direction | step | 根拠 |
|---|---|---|
| **scale-up** | `+max(1, N×0.25)` (additive) | TCP-AIMD と同じ、conservative にスケール、4 step で倍増 |
| **scale-down** | `N×0.5` (multiplicative) | 5-cycle sustained idle で発火 (§3.1)、無負荷状態と判定された worker を半減して Redis / DB 接続を返却 |

doubling は spike 時に高速だが overshoot で oscillation を起こしやすく、TCP slow-start の経験から AIMD の方が安定。

**Controller の time 依存性**: scale-up cool-down と sustained-idle カウントには `Clock` interface を注入する設計とし、unit test で fake clock を差し替えて deterministic に挙動を検証する (#1123 で interface 定義)。実装は `clockwork.NewRealClock()` 相当を default に持つ。

### 3.4 cool-down: 1 秒

scale event 発火後の **連続発火抑止 interval は 1 秒** (scale-up / scale-down 両方とも対称)。

- queue は ms スケールで変動するが、1 秒の遅延は drain time 全体 (10s-60s) に対し許容範囲
- Redis ZCARD を毎 100ms で叩く負荷は許容できないため、観測周期 = cool-down = 1s で統一
- cool-down は「発火後の sleep」のみ。scale-down 発火の **gate** には別途 §3.1 の 5-cycle sustained-idle hysteresis があり、こちらは「無負荷の確証を待つ」目的で機能が分離されている (cool-down ≠ sustained-idle)。

### 3.5 multi-process 協調: 初期 release は no coordination

同一 Redis を共有する複数 mk-go process が独立に auto-scale すると、合計 worker 数が想定を超える (multi-pod 環境):

- pod 1 が maxWorkers=128 までスケール
- pod 2 も独立に maxWorkers=128 までスケール
- → cluster 合計 256 worker、DB pool 飽和

初期 release では **協調なし** とし、operator が **per-process budget を自分で割る前提** で `maxWorkers` (per-queue) と `maxWorkersGlobal` (per-process 全 queue 合計) を設定する。cluster-wide budget の協調機構 (Redis 上の lease token 等) は将来 issue (#1120 tracker の「非ゴール」セクションに明示)。

multi-pod 運用への現実的アドバイス:
- 「pod 数 × per-pod `maxWorkersGlobal` ≦ DB pool size × 0.8」を operator が確認
- `maxWorkersGlobal` 設定時は **minWorkers floor を引いた残りが scaling headroom** になる点に注意:
  - 1 pod あたり常時 `minWorkers × len(autoScaled queues)` worker が起動 (e.g., 7 queue × min=4 = **28 worker は idle 時でも常時占有**)
  - `maxWorkersGlobal = 32` だと headroom = `32 - 28 = 4` worker しか scaling に使えない → deliver spike 時にほぼスケールできない
- **現実的な例**: 3 pod、DB pool=240 → per-pod `maxWorkersGlobal=64` (合計 192 ≦ 192 = DB pool × 0.8、各 pod の scaling headroom = `64 - 28 = 36` worker)
- DB pool が小さい (< 100) 環境では auto-scale の恩恵が薄いため、固定 `deliverJobConcurrency` の運用が現実的

### 3.6 既存 knob との優先順位

```
個別 knob (deliverJobConcurrency 等)
    > maxWorkers
        > controller (auto-scale)
```

- `deliverJobConcurrency: N` が明示設定 → controller は当該 queue を **管理対象から外す**、roster は N 固定で動作 (handler が戻らない worker を隔離している間だけ実際に走る worker は N を超えうる。§5.1.1)
- `maxWorkers: M` 設定 → controller の per-queue 上限として使う (§2)
- 両方未設定 → `DefaultMaxWorkers` (§2 で定義、per-queue) を採用
- `maxWorkersGlobal: G` (optional) → 全 queue worker 合計の hard cap (§3.2)

config struct での「未設定」表現は **`*int` ポインタ型 + `nil` = 未設定** で判定する (mk-go 既存 pattern `internal/config/config.go` の `DeliverJobConcurrency *int` 等と同じ)。`0` を「無制限」or「禁止」の sentinel に再利用しない (operator が `maxWorkersGlobal: 0` を書いた意図が判別可能になるよう)。

**startup validation** (#1125 で実装、operator の誤設定を起動時に検出):

- `maxWorkersGlobal` 設定時、`minWorkers × len(autoScaledQueues) ≤ maxWorkersGlobal` を満たさない場合は起動失敗 (= floor だけで cap を超える設定は無意味)
- `maxWorkers < minWorkers` の場合は起動失敗
- 違反時のエラーメッセージで `maxWorkers` / `minWorkers` / `maxWorkersGlobal` / auto-scaled queue 数を明示

`len(controllers) == 0` (全 queue に個別 knob 指定された) のケースでは controller goroutine を一切起動しない (= 完全に従来挙動と同じ)。

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
│  │  - mk_job_workers_active{queue}                     │   │
│  │  - mk_job_workers_quarantined{queue}                │   │
│  │  - mk_job_queue_pending{queue}                      │   │
│  │  - mk_job_dispatch_wait_seconds{queue}              │   │
│  │  - mk_job_scale_events_total{queue, direction}      │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

### 4.1 controller lifecycle

```
Server 起動時:
  if cfg.JobQueueAutoScale:
    # 個別 knob を持つのは deliver / inbox / relationship の 3 つだけ。
    # export / push / webhook / objectStorage には knob が無いので常に管理対象。
    for queue in [deliver, inbox, relationship]:
      if !cfg.<queue>JobConcurrency.IsSet():
        managed += [queue]
    managed += [export, push, webhook, objectStorage]
    for queue in managed:
      controllers[queue] = NewAIMDController(min, cfg.MaxWorkers, cooldown, clock)
      go controllers[queue].Run(ctx)  # 1s tick で observe + decide

ticker (per controller, 1s):
  depth = Redis.ZCARD(queue)
  current = driver.WorkerCount(queue)   // mkq では生存 Worker 数 (§5.1.1)
  action = controller.Observe(depth, current)
  if cfg.MaxWorkersGlobal != nil and globalSumWouldExceed(action, *cfg.MaxWorkersGlobal):
    action = NoOp  # global cap で reject
  switch action:
    case ScaleUp(n):
      driver.Resize(queue, n)
      metrics.IncScaleEvents(queue, "up")
    case ScaleDown(n):
      driver.Resize(queue, n)
      metrics.IncScaleEvents(queue, "down")
    case NoOp:
      ;

runtime kill switch (operator が SIGHUP / admin endpoint で発火):
  for c in controllers:
    c.Disable()       # ticker 停止、worker 数は現状維持 (= 固定値運用に degenerate)
  # operator は次の restart で config の jobQueueAutoScale: false を読み直す

Server 停止時:
  cancel(ctx)         # controller goroutine 終了
  driver.Close()      # worker graceful drain
```

runtime kill switch は **auto-scale が cluster を喰い尽くして restart も困難になった非常時** の脱出経路。SIGHUP handler は将来 issue (#1125 wiring 段階で実装)、初期 release では `jobQueueAutoScale: false` + restart で代替。

### 4.2 cost-bounded design

連合 flood / retry storm / runaway webhook 等で controller が cluster を喰い尽くす事故を防ぐため、以下の **多層防御**:

1. **`maxWorkers` hard cap** — controller は cap を超えてスケールしない (cap が縛るのは roster。合計は §5.1.1 の式で決まる)
2. **enqueue 側 backpressure** (将来 issue) — queue depth > 閾値時に inbox HTTP が 503 Retry-After で送信側に押し戻す
3. **host-level circuit breaker** (既存 #1067 系の拡張、将来 issue) — 落ちてる相手への retry が cluster を食わない
4. **per-queue scope** — 1 queue spike が他 queue の budget を奪わない
5. **panic switch** — `jobQueueAutoScale: false` 一発で controller off、roster は固定値運用に戻せる。**supervisor は autoscale と独立に動く**ので、隔離機構まで止めるには `queueStuckWorkerSeconds` に負値を入れる (§5.1.1)

## 5. multi-driver 整合 (mkq / asynq)

### 5.1 mkqdriver

#### 制約: mkq library 側に Resize API は無い

`github.com/shiroha-a/mkq` (現在 v1.0.6) の `worker_options.go` では `concurrency int` が `mkq.NewWorker` 構築時に固定される設計で、起動後の動的変更 API は存在しない。本 ADR では mkq library 自体には変更を加えず、**mkqdriver layer に Worker 群を管理する pool-of-workers 層を追加** する方針を採る (mkq library への PR は別途中長期検討、本 tracker scope 外)。

#### 実装: pool-of-Workers 方式

`mkqdriver.Server` 内に **per-queue で複数の mkq.Worker を保持する pool 層** を追加 (#1124):

```
mkqdriver.Server
 ├─ pools map[queue] *workerPool
 │   └─ workerPool
 │       ├─ workers    []*workerHandle  (生存 roster。各 Worker は WithConcurrency(1) 固定)
 │       ├─ quarantine []*workerHandle  (詰まって roster から外したもの、§5.1.1)
 │       ├─ desired    int              (最後に要求された roster サイズ)
 │       └─ mu sync.Mutex
 │
 └─ Resize(queue, n) error:  (desired = n として reconcile)
     - reconcile が先に走り、閾値超過の Worker を roster から quarantine へ移す (§5.1.1)
     - n > roster: 不足分の mkq.Worker を新規起動 (WithConcurrency(1))
     - n < roster: 余剰 Worker に `Stop(ctx)` を呼ぶ。**in-flight job の完了は待たない** — ctx が切れて cancel され、next pickup で retry される (`TestServer_ResizeDown_CancelsInFlight`)
     - n == roster: no-op
```

各 Worker を `WithConcurrency(1)` で起動して個別 Worker 単位で start / stop することで、library 側の API を変えずに **driver layer から動的 scale を実現** する (細粒度 control 重視、library への侵襲ゼロ)。

`Server.Shutdown` は全 pool の Worker (roster と quarantine の両方) の `Stop` を sync.WaitGroup で待つ。

#### 5.1.1 handler から戻らない Worker の扱い (#2657)

1 Worker = 1 dispatcher goroutine なので、**handler から戻らなくなった Worker は
二度と `awaitMarker` に到達せず、キューの capacity を無言で 1 本削る**。本番の
inbox で 4 本すべてがこの状態になり、`len(workers)` を返していた `WorkerCount` は
「4 本健全」と報告し続けた。scale-down が末尾 (= 直前に autoscale が足した唯一
健全な Worker) を落とす実装だったため、autoscale は健全な 1 本を作っては 6 秒で
殺す病的サイクルに入った。

詰まりが handler の中で起きていることは本番で確認した。`bull:inbox:active` に `ap:inbox` の job が 4 件、18-28 時間掴まれたまま残っており、
各 job の lock TTL が 30 秒の `lockDuration` に対し 23-30 秒残っていた。mkq で
`ExtendLock` を撃つのは heartbeat goroutine だけで、それは `runHandler` が戻った
直後に (finalise より前に) 畳まれる。lock が延長され続けている = **handler の中に
いる**。したがって handler の出入りを計測すれば足り、mkq 側に liveness API を足す必要は無い (v1.0.6 の `Worker` が
公開しているのは `Stop` だけ)。

対処は `reconcileLocked` に集約してある。

1. **隔離 (quarantine)**: handler の実行が閾値を超えた Worker を roster から外し、
   代わりを spawn する。**停止しない** — 閾値超過は詰まりの証明ではなく、`Stop`
   すると in-flight job が cancel されて retry に回り、閾値より長い job が永久に
   完了しなくなるため。**数を絞らずに必ず外す**のは、roster に残すと
   `len(workers)` と生存数がずれ、「増やせと言ったのに縮む」が起きるから
2. **復帰 (reinstate)**: 隔離後に **1 件でも job を完了した** Worker は生きている
   ので roster に戻す。**「今 idle か」では判定しない** — mkq は `moveToFinished`
   が返す prefetch をそのまま次の handler に渡すので、忙しいキューでは job と job
   の間の idle がマイクロ秒しかなく、supervisor の周期ではまず捉えられない。
   これを release 条件にすると、遅かっただけの健全な Worker が永久に隔離されたまま
   枠を食い潰す。戻した Worker は **復帰時に握っていた job を必ず完走する**
   (庇いが外れるのはその job を終えてから)。庇いは 1 件ぶんで、その後も余剰なら
   通常の scale-down 対象に戻る — 「一度隔離されたら以後ずっと守られる」ではない
3. **`WorkerCount` は生存数**: roster のうち閾値を超えていないものを数える。
   `Resize` も reconcile 経由なので勘定は一致する
4. **scale-down の対象選択**: 末尾固定をやめ、idle を先に選ぶ。閾値超過の Worker は
   すでに roster にいないので、「詰まっているから停止する」判断はどこにも無い

回収は **autoscale とは独立した supervisor goroutine** (既定 30 秒間隔) が回す。
`jobQueueAutoScale` は opt-in で、無効なら `Resize` は一度も呼ばれない。詰まりの
回収を `Resize` 経路にだけ置くと、既定構成では capacity が減ったまま二度と戻らない。

##### 閾値はキューごと

| キュー | 閾値 | 根拠 |
|---|---|---|
| deliver / inbox / relationship / push / webhook | 30 分 | 恒久的に戻ってこない Worker の回収が狙いなので、短くしても得るものが少ない。#2657 は 28 時間戻らなかった |
| export / objectStorage / maintenance | 追跡しない | 1 job が分単位でページングするのが正常。`cleanRemoteFiles` は最大 10000 バッチ x 500ms (83 分) |
| 上記以外 (既定一覧に無いキュー) | 追跡しない | job の長さを想定できないものに閾値を当てても誤検知しか生まない |

`queueStuckWorkerSeconds` に正値を入れると全キューにその値が効き、負値で機能ごと
無効になる。

**追跡対象にも長い job は残る。** `deleteAccount` は maintenance ではなく
**deliver** に載っており (`EnqueueDeleteAccount`)、100 件ごとに 250ms 空けるので
70 万ノート規模で 30 分を超える。inbox も resolver の再帰で理論上 40 分を超えうる。
どちらも隔離されるだけで job は cancel されないので実害は限定的だが、閾値を
短くするとその状態が定常化する。

##### 実効の並列度と上限

**閾値は「健全な job の上限」ではない。** `safehttp` が切るのは 1 リクエスト
10 秒であって 1 job ではなく、inbox の handler は resolver 経由で最大
`resolveRecursionLimit` (256) 段の逐次 fetch を回しうるので、理論上の worst case は
40 分を超える。job 単位の期限は #2658 で別途入れる。

**隔離した Worker は動き続ける。** 止められない (handler が戻らない以上、`Stop` は
`stopWorkerTimeout` = 30 秒だけ待たされたうえで goroutine を残して返る) し、
遅かっただけなら止めるべきでもない。したがって代わりを立てた分だけ
**実効の並列度は設定値を超える**。上限は
`到達した最大 worker 数 + max(設定値, 4)` で、autoscale 無効なら
`設定値 + max(設定値, 4)`、有効なら `max(設定値, maxWorkers) + max(設定値, 4)`
にあたる (peak は設定値から始まる単調非減少値なので、`maxWorkers` を設定値より
小さくしても下がらない)。
これを超えると roster を縮めて Error を出す (5 分に 1 回まで)。

**幅は `desired` ではなく設定値に紐づける。** `desired` は autoscale が動かす値で、
しかも autoscale の入力はこの幅を通した生存数から来る。幅を `desired` から計算すると
「隔離が増える → 生存数が減る → autoscale の目標が下がる → 幅が縮む → さらに減る」
という帰還ループになり、**scale-up 要求のたびに roster が縮んで最終的に 0 になる**
(`desired=16` / 隔離 20 で 3 tick、実測)。

隔離された Worker は `awaitMarker` に戻って BZPopMin 接続を握り続けるので、その分の
Redis 接続を見込んでおかないと #2657 の引き金になった
`resource temporarily unavailable` を自分で再現することになる。**`redisForJobQueue.poolSize`
を明示していない場合に限り** `workerPoolSize` が隔離ぶんを上乗せして自動確保する
(go-redis の `PoolSize` は上限であって事前確保ではないので、広げても普段のコストは
無い)。**明示している構成では上乗せされない** — §2 のように `poolSize` を書くなら、
追跡対象キューについては `maxWorkers + max(<queue>JobConcurrency, 4)` を賄える値に
すること。

この上限があるので、`maxWorkers` / `maxWorkersGlobal` / 個別 knob は **「同時に
走る Worker 数の hard cap」ではなくなった**。それらが縛るのは roster (= 仕事を
取れる Worker) の数で、隔離中の Worker はそこに含まれない。合計の上限は上の式で
決まる。

##### 検出できないもの

dispatcher が handler の**外** (mkq 内部の Lua 呼び出しや BZPOPMIN) で wedge した
場合は idle と区別できない。#2657 で観測した詰まりは handler 内だったので対象外と
した。キュー深さと組み合わせた粗い検出は paused queue や rate limit で偽陽性が出る
ため採らない。恒久的な答えは handler 側に期限を付けること (#2658)。

#### trade-off

- **Pro**: mkq library 変更不要、upstream とすぐ整合
- **Pro**: scale-down の granularity が 1 worker 単位、stop も Worker 単位で graceful
- **Con**: N=128 のとき mkq.Worker が 128 個動く = WithConcurrency(128) の 1 Worker より overhead 微増 (内部 dispatch loop が 128 本動く)
- **Con (定量見積もり)**: 1 Worker あたり ~2 goroutine (dispatch + heartbeat) + 1 Redis 接続。N=128 で:
  - Memory: ~256 goroutine × 8KB initial stack ≒ **2MB 追加** (WithConcurrency(128) 1 Worker 比、~1MB 増)
  - Redis 接続: 128 本 (どちらの方式でも同じ、go-redis pool 上限要調整)
  - 詳細 overhead は #1124 の integration test で測定、許容できない場合は mkq library に Resize API を追加する PR を別途検討

### 5.2 asynqdriver

asynq library は `Concurrency` を Server 構築時に固定する設計で、動的 Resize に対応する API がない (upstream に PR を出す or fork が必要)。

初期 release では **asynqdriver は auto-scale 対象外** とし、`jobQueueAutoScale: true` + `jobQueueDriver: asynq` の組み合わせは config 検証で reject (or warning + 固定値 fallback)。

将来 asynq に Resize 相当の API が入った時点で対応する (`#1120 tracker` の「将来」項目)。

## 6. observability

### 6.1 Prometheus metric (#1122 で先行 export)

[Prometheus metric naming convention](https://prometheus.io/docs/practices/naming/) に従い、gauge は無 suffix or unit suffix、counter は `_total` suffix:

| metric name | type | labels | 説明 |
|---|---|---|---|
| `mk_job_workers_active` | gauge | queue | 各 queue で**仕事を取れる** worker 数。goroutine 数ではない (mkq では隔離中の worker を除く、§5.1.1) |
| `mk_job_workers_quarantined` | gauge | queue | 閾値超過で pool の外に退けてある worker 数 (mkq のみ、他は常に 0)。**0 でない状態が続いていたら handler がブロックしている** |
| `mk_job_queue_pending` | gauge | queue | Redis ZCARD 値 (pending job 数) |
| `mk_job_dispatch_wait_seconds` | histogram | queue | enqueue → dispatch までの待ち時間 |
| `mk_job_processing_seconds` | histogram | queue, status | job 処理時間 (success / failure) |
| `mk_job_scale_events_total` | counter | queue, direction | auto-scale 起動回数 (up / down) |

(gauge に `_count` を使うと counter の `_total` と意味的に混同しやすいため避ける。`_active` は「現在値」を明確に示す慣例 suffix)

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

- 新 config の説明 (`jobQueueAutoScale` / `maxWorkers` / `maxWorkersGlobal` / `minWorkers` / `autoScaleCooldownSeconds`)
- 既存 knob との優先順位 (§3.6)
- multi-pod 運用での `maxWorkersGlobal` 推奨値計算式 (§3.5)
- panic switch (auto-scale off) 手順 (§9.3)

## 7. tests strategy

### 7.1 controller unit test (#1123)

`internal/queue/autoscale/aimd_test.go` で AIMDController の state machine を table-driven test で全 transition を網羅:

- scale-up trigger (depth > N × 4 で 1 観測即発火、§3.1)
- scale-down trigger (depth == 0 が 5 cycle 連続したときのみ発火、transient idle では発火しない、§3.1)
- cool-down 中の no-op (1 秒以内の連続発火を抑止、§3.4)
- max bound 到達時の cap (`maxWorkers` を超えない)
- min bound 到達時の floor (`minWorkers` 以下にならない)
- floor 復帰 (`minWorkers` を下回ったら depth に関わらず戻す。`minWorkers: 0` では発火しない、§3.1)
- `maxWorkersGlobal` 到達時の reject (controller 側で NoOp、§3.2)
- time injection (fake Clock での deterministic test、§3.3)

実 Redis 不要、pure logic test。

### 7.2 driver integration test (#1124)

`internal/queue/driver/mkqdriver/integration_test.go` に追加:

- `TestServer_ResizeUp_ProcessesMoreInParallel` — Resize(2 → 8) で並列度が上がることを確認
- `TestServer_ResizeDown_CancelsInFlight` — Resize(4 → 1) で **in-flight job は完了を待たず cancel される** ことを固定する (next pickup で retry される前提)。assert は「2 秒以内に返る / 3 件以上 cancel / 完了 0 / WorkerCount が 1」
- `TestServer_ResizeRace` — 同時 multiple Resize 呼び出しで panic / leak しない
- `TestSupervisor_ReplacesWedgedWorkerAndKeepsDraining` — 戻らない handler で Worker を
  詰まらせても、差し替えられた Worker がキューを捌き続けることを固定する (§5.1.1)
- `TestSupervisor_ReinstatesWorkerThatWasMerelySlow` — 隔離が in-flight job を
  cancel しないこと (= 遅かっただけの job は完走し、Worker が pool に戻る) を固定する
- `TestSupervisor_ReinstatesWorkerOnABusyQueue` — 常に仕事があるキューでも復帰
  できることを固定する。idle の観測で判定していると、prefetch で job が連鎖する
  ため永久に隔離されたままになる
- `TestSupervisor_DoesNotTrackBatchQueues` — export のような batch 系キューでは
  長い handler を隔離しないことを固定する (実効の並列度が上がり続けるのを防ぐ)
- `TestResize_DoesNotEvictTheOnlyHealthyWorker` — 詰まった Worker がいる状態の
  scale-down が健全な Worker を先に落とさないことを固定する

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
| 1 | #1122 metric export | yes | 常時 export、controller 未稼働 |
| 2 | #1123 AIMD controller library | yes | library のみ、配線なし |
| 3 | #1124 mkqdriver Resize | yes | Resize API 追加のみ、auto 起動なし |
| 4 | #1125 queue_factory wiring | yes | `jobQueueAutoScale: true` 時のみ起動、default false |
| 5 | #1126 queue-bench report | data only | merge gate ではなく documentation |

各 PR は revert 可能、controller の挙動に問題が出ても #1125 を revert すれば固定運用に完全復帰する。

## 9. migration ガイド

### 9.1 既存 operator (auto-scale 不要)

何もしなくて良い。`jobQueueAutoScale` は default false なので、既存設定はそのまま動作する。

### 9.2 auto-scale を試す

1. config に `jobQueueAutoScale: true` を追加 (他は何も変えない)
2. 既存の `deliverJobConcurrency` 等は **削除する** (controller に管理させる、残すと固定値が優先)
3. `maxWorkers: <値>` を per-queue 上限として設定 (default: `runtime.NumCPU() × 16`、明示推奨)
4. multi-pod 環境の場合のみ `maxWorkersGlobal: <値>` (全 queue worker 合計 cap) も設定
5. mk-go を再起動
6. grafana / prometheus で `mk_job_workers_active{queue}` が動的に変化することを確認
7. `mk_job_scale_events_total{queue,direction}` で scale 発火頻度を観測、必要なら `maxWorkers` を調整

### 9.3 panic switch (障害時)

```yaml
jobQueueAutoScale: false      # この 1 行で固定値 fallback
deliverJobConcurrency: <値>   # 障害発生前の固定値、または runtime.NumCPU() × 8 を目安
inboxJobConcurrency:   <値>   # 同上 (inbox は I/O 比が低いので deliver の半分が目安)
queueStuckWorkerSeconds: -1   # 詰まり検出まで止める場合のみ (§5.1.1)
```

再起動で固定運用に戻る。controller goroutine も終了するため leak しない。

**`jobQueueAutoScale: false` だけでは詰まり検出は止まらない。** supervisor は
autoscale とは独立に動くので、worker 数を厳密に固定したいなら
`queueStuckWorkerSeconds: -1` も要る (§5.1.1)。**経験則の目安値** (8-core 想定): `deliverJobConcurrency: 64` / `inboxJobConcurrency: 32`。実 workload で問題が出ない場合は default (`16`) のまま `jobQueueAutoScale: false` でも可。

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
