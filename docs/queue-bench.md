# Queue bench (#563)

ジョブキュー配送スループットを **3 driver** (Misskey TS BullMQ / mk-go asynq / mk-go mkq) で公正比較するベンチマーク基盤。HTTP latency 用 `tests/bench/` とは別運用。

## 計測対象

### Outbound (deliver job throughput)

各 stack の local user → `blackhole` 受信機への AP deliver job スループット。

- 各 stack に **dummy follower 100 名** (host=`blackhole`, ユニーク inbox URL) を pre-seed
- driver が `notes/create` (visibility=public) を 100 回叩く → fan-out で **10,000 deliver job** が enqueue
- `blackhole` は POST を全て 204 で即返すので receiver overhead 無し
- 計測: post 開始から blackhole hit が expected 数に達するまでの drain 時間 / RPS

### Inbound (inbox job throughput)

`faker` (Go HTTPS, AP HTTP signature 直接) → 3 receiver inbox に signed activity を blast。

- 各 receiver に faker actor を pubkey 込みで pre-seed (外部 fetch 排除)
- `faker` は **pre-sign 並列化** で sender 側を律速から外し、receiver の verify+enqueue が bottleneck になるよう設計
- 計測: faker → receiver の RPS (= 受信→200 までの rate) と post-send drain time

#### Activity type (`INBOUND_ACTIVITY_TYPE`)

faker payload は 2 種類から選択 (env で driver_inbound に渡す):

- `create` (default): `Create(Note)` — handleCreate 経路を計測 (#569)
- `announce`: `Announce(object=<receiver-local note URI>)` — handleAnnounce 経路を計測 (#1158)
  - `seed` が各 stack に benchsender 経由で 1 件 target note を作成し、URI を `seed.json` に書き出す
  - faker は per-(target, index) で activity.id を完全ユニーク化 (target hash 含む) して dedup 干渉を回避

## 実行

```bash
# 1) 3 stack + blackhole + faker を up (5-10 min、初回 build を伴う)
make queue-bench-up

# 2) seed (user / follower / faker actor を DB 直挿入)
#    meta.federation='all' を強制設定 → app コンテナを restart
make queue-bench-seed

# 3) outbound 計測
make queue-bench-outbound

# 4a) inbound 計測 (default: Create 経路)
make queue-bench-inbound

# 4b) inbound 計測 (Announce 経路、#1158 等で利用)
INBOUND_ACTIVITY_TYPE=announce make queue-bench-inbound

# 5) report 生成 (tests/queue-bench/results/queue-report.md)
make queue-bench-report

# まとめて: queue-bench-all (seed → outbound → inbound → report)
make queue-bench-all

# cleanup
make queue-bench-down
```

## 結果ファイル

`tests/queue-bench/results/`:

- `outbound.json` — 生データ (per-stack drain time / hits / depth time series)
- `inbound.json` — faker.send 統計 + per-receiver drain (最後に走った activity type の値で上書き)
- `inbound-announce-after.json` — Announce 経路の参考値 (PR #1158 適用後、2026-05-21)
- `queue-report.md` — markdown 比較表

### Announce 経路 reference 値 (PR #1158 マージ後、2026-05-21)

10000 req × 3 target、concurrency 128 で計測。app コンテナは develop @ `9adc836` (PR #1161 マージ後)。

| Stack | Send Duration | Effective rps | Drain time | Peak queue depth | Notes |
|---|---|---|---|---|---|
| mk-go (asynq) | 3.66s | 2729.9 | 47.15s | 9827 | 受信→202 まで 3 秒台 |
| mk-go (mkq) | 3.46s | 2891.3 | 63.43s | 9912 | 同上 |
| Misskey TS | 10.46s | 956.4 | 18.08s | 0 | worker が send と同期処理 |

観測:

- mk-go (async/mkq) は **TS の 2.9x** の send-side throughput (Create 経路の 3.0x と同水準)
- Announce 経路は Create 経路と比べて -4〜-12% (HTTP signature verify + target note resolve + renote create の overhead)
- mk-go は peak depth 9800+ まで queue を積んで worker が drain (47-63s)、TS は send と同期で peak=0

PR #1158 の effect (handleAnnounce hook async 化) を before/after で数字化する作業は、bench tool の startup 安定性 (3 stack 並列起動で random に 1 stack が ok=0 になる) が解消できていないため follow-up で別途実施予定。

## 設計メモ

### なぜ TS sender でなく faker?

inbound bench で TS instance を sender に使うと、TS 側の deliver throughput が上限になり、receiver inbox 性能を測れない。faker は Misskey 実装非依存の Go HTTPS server で:

- 固定 RSA-2048 keypair + 固定 actor URI (`https://faker/users/bench-sender`)
- HTTP signature 計算は **bench 開始前に並列で pre-sign**
- send phase は単純な HTTP POST blast で sender 側がボトルネックにならない

### 計測限界

- inbound で receiver workers が即処理する場合、queue depth は polling 粒度 (50ms) では peak ≈ 0 に見える。実効 throughput は faker.send rps が真の receiver ingest+process rate
- outbound は blackhole hits (delivered count) が primary signal なので polling race の影響を受けない

### Federation flag 注意

mk-go は新規 DB 初期化時 `meta.federation='none'` (= 連合無効) で立ち上がる。seed が DB 直接 UPDATE で `federation='all'` にしたあと、app の meta cache (5min TTL) を再読み込みさせるため `make queue-bench-seed` の最後で `app-asynq` / `app-mkq` / `app-ts` を restart する。

### Network allowlist

mk-go の SSRF 防止 (`allowedPrivateNetworks`) は production default で private IP を block する。bench 内の `blackhole` / faker / 他 stack は Docker network の private IP なので、bench config (`tests/queue-bench/common/mk-{asynq,mkq}.yml`) で `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` を allowlist 化している。

## 関連

- #560 (HTTP bench 結果トラッキング)
- #561 (HTTP bench nginx fix)
- #562 (HTTP bench rate limit fix)
- #413 (パフォーマンス改善ロードマップ)
