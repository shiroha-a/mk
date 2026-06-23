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
├─ ts    (image: misskey/misskey:2026.5.4, config: tests/diff/ts.yml)
│   ├─ postgres-ts / redis-ts
└─ diff-runner (profiles:[test], pytest + requests)
     MKGO_URL=http://mkgo:3000  TS_URL=http://ts:3000
```

- **API-only (HTTP, no TLS/federation)**: runner は両 backend を `:3000` で直接
  叩く。WebAuthn/secure-context は不要なので nginx TLS 層は省く。
- **version**: mk-go は現行 2026.6.0、TS は入手可能な最寄りの公式 image
  2026.5.4。1 minor 差 (= 直近の 2026.6.0 追従分) のノイズは ignore-list と
  endpoint 選定で吸収する。
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
| `tests/diff/test_endpoints.py` | endpoint 別の差分テスト (現状 meta + note packing の PoC) |
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
issue 消化ワークフロー (実装 → 敵対的レビュー → PR → CI → squash-merge) で潰す。
ハーネス自体の endpoint coverage 拡張は #2089 で追う。

## CI 方針

dropin/playwright 同様、**PR の required check には含めない** (2 backend 起動 +
image pull で重く、external image の flaky 要素もある)。nightly か手動
(`make diff-test`) 運用とする。diff-core の unit test だけは軽いので、必要なら
別途 lint/test に組み込める。

## 検証状況

初回 bring-up で end-to-end 動作を確認済 (fresh stack):

- diff-core unit test 13 件 + `test_meta_value_parity` + `test_note_packing_parity`
  = **15 passed**。
- `test_note_packing_parity`: mk-go と TS の packed note が (instance noise を除き)
  **値レベルで一致** することを確認。
- `test_meta_value_parity`: meta は version-gap (2026.6.0 で増えた field) と instance
  state (mediaProxy host / proxyAccountName) のノイズを `META_IGNORE` で吸収した上で
  pass。これらは harness が初回に検出した差分で、version-matched TS に切替えたら見直す。

## 既知の制約・今後

- TS image が 2026.5.4 のため 1 minor 分のノイズが出る。完全 version 一致が必要に
  なれば third_party/misskey (2026.6.0) からの source build に切り替える。
- 初回 `make diff-test` は ignore-path の調整 (endpoint 固有ノイズの洗い出し) を
  伴う。PoC endpoint で当たりを付けてから coverage を広げる。
- auth が要る endpoint は `Client.ensure_admin` の token を使う。複雑な seed
  (follow/反応/連合) が要る endpoint は fixture を足して対応する。
