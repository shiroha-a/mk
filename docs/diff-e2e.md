# Differential e2e diff harness (#2089)

mk-go と Misskey TS の**実 API レスポンスを並べて diff** し、静的コード比較や
entitycompat golden gate では拾えない**値レベルの乖離**を検出するハーネス。

## なぜ必要か

既存の互換性検証には次の層がある:

- **entitycompat golden gate**: error code/id/HTTP status と entity field の
  **presence (shape)** を golden と突き合わせる。
- **dropin / playwright e2e**: TS ↔ mk-go 切替が成立するか、フロントから見た
  挙動が壊れないかを検証する。

これらは「field が**存在するか**」「shape が合うか」は見るが、「field の**値**が
upstream と一致するか」(計算結果・正規化・順序・条件分岐の差) は検証しない。
本ハーネスはそこを埋める: **同一の入力に対する mk-go と TS の出力 JSON を field
単位で diff** し、instance 固有のノイズ (id/時刻/host 等) を除いた残差を parity
乖離の候補として報告する。

## アーキテクチャ

```
docker-compose.diff.yml  (隔離 stack、production UDS には触れない)
├─ mkgo  (build: tests/federation/common/Dockerfile.mkgo, config: tests/diff/mkgo.yml)
│   ├─ postgres-mk / redis-mk
├─ ts    (image: misskey/misskey:2026.7.0, config: tests/diff/ts.yml)
│   ├─ postgres-ts / redis-ts
└─ diff-runner (profiles:[test], pytest + requests)
     MKGO_URL=http://mkgo:3000  TS_URL=http://ts:3000
```

- **API-only (HTTP, no TLS/federation)**: runner は両 backend を `:3000` で直接
  叩く。WebAuthn/secure-context は不要なので nginx TLS 層は省く。
- **version**: mk-go・TS ともに **2026.7.0** で一致している。かつては公式 image が
  1 minor 遅れており version-gap のノイズを ignore-list で吸収していたが、その必要は
  無くなった。追従直後で公式 image が未公開の期間だけ、再び gap が生じうる。
- **隔離 (重要)**: compose 先頭で `name: mkdiff` を指定し専用 project に固定する。
  これが無いと default project は directory 名 `mk` になり、**本番 UDS stack
  (compose.uds.yaml, 同じ project `mk`) の `mkgo` サービスと衝突して本番コンテナを
  recreate してしまう** (整備中に実際に踏んで本番 mkgo を一時 clobber → 即復旧した)。
  `mkdiff-*` という別 namespace で own network/volumes を持ち、`make diff-down`
  (`down -v`) で完全破棄。本番 (UDS) には一切触れない。

- **one-shot**: seed は Misskey の bootstrap (admin/accounts/create が無認証で通るのは
  user 0 件のときだけ) を使うので、`make diff-test` は `make diff-up` 直後の fresh
  state に対して 1 回流す前提。再実行は `make diff-down && make diff-up` で作り直す。

## 使い方

```bash
make diff-up      # mkgo + ts (+ それぞれの DB/Redis) を build + 起動
make diff-test    # diff-runner (pytest) を両 instance に対して実行
make diff-logs    # ログ追尾
make diff-down    # stop + volume ごと破棄
```

`make diff-test` 失敗時、pytest の assertion message に `format_diffs` が
`[kind] $.path: mkgo=... ts=...` 形式で乖離を列挙する。

## 構成要素

| ファイル | 役割 |
|---|---|
| `tests/diff/diff_core.py` | JSON 値 diff の中核。ignore-list 付き再帰比較。**stdlib のみ**で unit-test 可能 |
| `tests/diff/test_diff_core.py` | diff-core の unit test (`python3 tests/diff/test_diff_core.py` で単体実行) |
| `tests/diff/conftest.py` | `mkgo` / `ts` fixture (Client) + health 待ち。env URL から接続 |
| `tests/diff/test_endpoints.py` | endpoint 別の差分テスト (30 件) |
| `tests/diff/{mkgo,ts}.yml` | 各 instance の config |
| `tests/diff/Dockerfile.runner` | pytest + requests の runner image |
| `docker-compose.diff.yml` | 2 backend + DB/Redis + runner |

## ignore-list 戦略

`diff_core.DEFAULT_IGNORE_KEYS` は 2 instance 間で必然的に異なる field を除外する
(id / createdAt / updatedAt / host / uri / url / token / publicKey / version 等)。
endpoint 固有のノイズ (meta の operator 設定、note の user オブジェクト等) は
各テストで `ignore_keys=` / `ignore_paths=` を足して吸収する。

方針は**保守的**: 「取りこぼし (本物の乖離を見逃す)」より「ノイズで埋もれて
本物が見えなくなる」方が害が大きいので、確実に instance 固有な field のみ無視し、
残差は人間が確認する。

## 検出した乖離の扱い

確認された値レベル乖離は **#2078 の sub-issue** として個別に起票し、通常の
issue 消化ワークフロー (実装 → 敵対的レビュー → PR → CI → rebase and merge) で潰す。
ハーネス自体の endpoint coverage 拡張は #2089 で追う。

## CI 方針

`.github/workflows/diff-e2e.yml` が **PR ごとに** `make diff-check` を実行する
(paths フィルタ付き、`workflow_dispatch` でも発火)。

dropin/playwright 同様、**required check には含めない** (2 backend 起動 +
image pull で重く、external image の flaky 要素もある)。CI 上の check 名は `diff`。

```bash
make diff-check    # down → up → healthy 待ち → pytest を通しで (クリーン DB 前提)
make diff-test     # スタックが既に上がっている場合
```

非ブロッキングを `continue-on-error` で実現しないこと (job が成功扱いになる)。

## カバレッジ

pytest の総数は 43 で、内訳は:

- **endpoint 比較 30 件** (`test_endpoints.py`) — mk-go と TS に同じリクエストを
  投げて値を突き合わせるもの
- diff-core の unit test 13 件 (`test_diff_core.py`) — 差分の取り方そのものの検証。
  stdlib のみなので `python3 tests/diff/test_diff_core.py` で単体実行できる

**「43 比較」ではない。** 実際に 2 backend を突き合わせているのは 30 件。

比較対象は meta / user (packing / rich profile / relation) / `i/me` /
note (packing / reaction / reply / renote / hashtag / state / poll) / clip / user list /
channel / antenna / drive file / drive folder / OAuth app / page / announcement /
emoji / flash / favorites / mute list / timeline (home / local / user notes /
followee) / locked follow request。

`test_meta_value_parity` の `META_IGNORE` は instance state (mediaProxy host /
proxyAccountName) のノイズを吸収するためのもの。version-gap 由来の除外は TS image を
2026.7.0 に揃えた時点で不要になったため、**残っているものが instance state 起因だけかを
追従のたびに見直すこと**。

## 既知の制約・今後

- 公式 image の公開は upstream release から遅れる。追従直後に version を厳密に
  合わせたい場合は third_party/misskey からの source build に切り替える。
- endpoint を足すときは ignore-path の調整 (endpoint 固有ノイズの洗い出し) が
  伴う。**ignore-list を安易に広げないこと** — 空振りさせると本物の乖離が埋もれる。
  追加時は `docs/divergence.md` に対応する記述があるかを確認する。
  `META_IGNORE` と `USER_IGNORE` は別定義で後者は前者を継承していないので、
  `policies` のような両方に現れるキーは両方へ足す必要がある。
- auth が要る endpoint は `Client.ensure_admin` の token を使う。複雑な seed
  (follow/反応/連合) が要る endpoint は fixture を足して対応する。
