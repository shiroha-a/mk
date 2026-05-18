# Auto-scale comparison bench (#1126)

Generated at: 2026-05-18T15:51:34.652793+00:00

## Setup

- Single mkq stack on localhost (1 postgres + 1 redis + 1 mk-go app)
- Burst: 3 notes × 10 followers = **30 deliver jobs / scenario**
- Driver: tests/queue-bench-autoscale/driver/bench-driver.py
- Per scenario: `compose down -v` between runs for clean DB / Redis state

## Scenarios

| Scenario | config | 期待 |
|---|---|---|
| `fixed16` | `deliverJobConcurrency: 16` / `inboxJobConcurrency: 16` | TS 互換 default、ベースライン |
| `fixed64` | `deliverJobConcurrency: 64` / `inboxJobConcurrency: 32` | 経験則上の I/O-bound optimal (8-core 想定) |
| `auto` | `jobQueueAutoScale: true` / `minWorkers: 4` / `maxWorkers: 64` | AIMD controller で動的伸縮 |

## Drain time (deliver burst)

| Scenario | Drain time | Throughput (jobs/s) | Hits |
|---|---|---|---|
| `fixed16` | 0.193s | 155.8 | 30 |
| `fixed64` | 3.923s | 7.6 | 30 |
| `auto` | 0.188s | 160.0 | 30 |

## Redis connections

アイドル時 / 負荷時の Redis CLIENT 数。pool-of-Workers 構造で 1 Worker = 1 BLPOP connection を保持するため、worker pool サイズが直接 connection 数に反映される。

| Scenario | Idle clients | Busy clients |
|---|---|---|
| `fixed16` | 47 | 50 |
| `fixed64` | 85 | 86 |
| `auto` | 34 | 35 |

## Post submit time

notes/create POST の **submit** にかかった時間 (= HTTP response 受け取り完了)。drain time に含まれる前段。fan-out enqueue が遅れていれば post 自体も遅延する。

| Scenario | Post submit |
|---|---|
| `fixed16` | 0.034s |
| `fixed64` | 0.037s |
| `auto` | 0.057s |

## Raw results

### `fixed16`

```json
{
  "scenario": "fixed16",
  "outbound_notes": 3,
  "followers_per_note": 10,
  "expected_deliver_jobs": 30,
  "post_submit_s": 0.034,
  "drain_time_s": 0.193,
  "drain_timed_out": false,
  "blackhole_hits": 30,
  "throughput_jobs_per_s": 155.8,
  "idle_redis_clients": 47,
  "busy_redis_clients": 50
}
```

### `fixed64`

```json
{
  "scenario": "fixed64",
  "outbound_notes": 3,
  "followers_per_note": 10,
  "expected_deliver_jobs": 30,
  "post_submit_s": 0.037,
  "drain_time_s": 3.923,
  "drain_timed_out": false,
  "blackhole_hits": 30,
  "throughput_jobs_per_s": 7.6,
  "idle_redis_clients": 85,
  "busy_redis_clients": 86
}
```

### `auto`

```json
{
  "scenario": "auto",
  "outbound_notes": 3,
  "followers_per_note": 10,
  "expected_deliver_jobs": 30,
  "post_submit_s": 0.057,
  "drain_time_s": 0.188,
  "drain_timed_out": false,
  "blackhole_hits": 30,
  "throughput_jobs_per_s": 160.0,
  "idle_redis_clients": 34,
  "busy_redis_clients": 35
}
```
