# テスト

> CI で実際に何が回っていて、落ちたとき何を疑うかは [CI で回る項目](ci.md) を参照。
> 本ドキュメントはテストの種類と書き方を扱う。


## テストの種類

| 種類 | 対象 | DB/Redis | 実行方法 |
|---|---|---|---|
| ユニットテスト | APIハンドラ、サービスロジック | モック | `go test ./internal/api/...` |
| 統合テスト | リポジトリ、Redis連携 | 実 PostgreSQL (`TEST_DB_*`) + Redis (testcontainers) | `go test ./internal/core/...` |
| E2Eテスト (Playwright) | フロントエンド操作 / API | 実DB + フロントエンド | `make playwright-test` (詳細は[Playwright](playwright.md)) |
| 連合テスト | mk-go ↔ 本物の Misskey TS の AP 通信 | Docker Compose多段 | `make federation-misskey-e2e` (起動から撤去まで通し。個別に叩くなら `-up` → `-test` → `-down`) |
| Drop-in e2e (pytest) | TS-A backend を mk-A に差し替えて state preservation 検証 | TS 2 instance + mk overlay | `make dropin-swap-test` (#365 / #367 / #372 / #374、詳細は[dropin-e2e.md](dropin-e2e.md)) |
| Drop-in frontend e2e (cypress) | 3 TS instance + mk overlay swap で frontend 視点の互換 | cypress + 3 TS + mk-A | `make dropin-frontend-swap-test` (#380 / #381 / #387 / #394、詳細は[dropin-frontend-e2e.md](dropin-frontend-e2e.md)) |
| Playwright e2e | mk-go と Misskey TS の両 backend で API/frontend 統合互換を検証 | Docker Compose 全部 | `tests/playwright/` 配下 (#744、370 spec。PR ごとに mk-go、upstream 追従時に TS backend) |
| 本家 backend e2e | Misskey 本家の `test/e2e/**` をテスト本体無改変で mk-go に向けて実行 | PostgreSQL / Redis + mk-go バイナリ | `make upstream-e2e` (#2347、25 ファイル 1245 テスト。詳細は[upstream-backend-e2e.md](upstream-backend-e2e.md)) |

## 手元の準備

**PostgreSQL は自分で用意する。** Redis は testcontainers が立ててくれるが、DB を使うテストの大半は外部の PostgreSQL に直接つなぐので、Docker があるだけでは `make test` は通らない (下記「testcontainers」参照)。

```bash
cp .env.test.example .env.test    # TEST_DB_* が入っている。必要なら編集する
```

`internal/testutil` が起動時にプロジェクトルートの `.env.test` を読み、既に設定済みの環境変数は上書きしない。**export で直接渡してもよい。** `.env.test` は `.gitignore` 済み。

接続先の PostgreSQL には `TEST_DB_NAME` (既定 `misskey_test`) のデータベースが要る。CI は service container を立てて同じ環境変数を渡している。

## 実行方法

```bash
# 全テスト (.env.test か TEST_DB_* で指した PostgreSQL に接続する)
make test

# 特定パッケージ
go test ./internal/api/notes/...

# レース検出 + カバレッジ (CIと同条件)
go test -race -count=1 -timeout 10m \
  -coverprofile=coverage.out -covermode=atomic ./...

# カバレッジHTMLレポート
go tool cover -html=coverage.out
```

## カバレッジ目標

| レベル | 閾値 | 説明 |
|---|---|---|
| CIゲート (最低ライン) | 90% | これを下回るとCIが失敗しマージ不可 |
| 推奨ライン | 95% | 通常のPRではここを目指す |
| 目標ライン | 100% | 新規パッケージや小規模パッケージで積極的に狙う |
| `internal/api/admin` | 80% | SMTP/queue/DB集計等の外部依存で 90% 未到達のため暫定緩和 (#260 以降) |
| `internal/testutil` | 0% | mock / test helper 専用、production code を含まないため閾値対象外 |
| `internal/server` | 0% | router.go (~2000 行) の wire 層中心、e2e/drop-in test で実挙動検証 (#462) |
| e2e | 0% | 統合 test 専用 |

CIではパッケージごとにカバレッジを計測し、閾値未達のパッケージがあればジョブが失敗する。CI は **4-way matrix shard** で並列実行され、ImportPath 順 modulo 分配で決定的にパッケージを割り当てる (約 4.7 分 → 1.5-2 分に短縮)。

## DB を使うテストの分離 (#2450)

`testutil.OpenTestDB` / `MustOpenTestDB` は**呼び出し元のパッケージ専用の PostgreSQL schema** に接続する (`internal/api/gallery` なら `internal_api_gallery`)。schema 名は呼び出し元から自動で決まるので、新しいパッケージも何もしなくても隔離される。

`go test` は**パッケージのテストバイナリを並行実行する**。CI は shard ごとに PostgreSQL を 1 つしか立てないため、共有すると一方の後片付けが他方の前提を壊す。実際に `internal/charttick` の `DELETE FROM "user"` が `internal/api/gallery` の所有者 user を消し、**Go を一切触っていない PR で CI が落ちた**。

守ること:

- **DB を読み書きするテストで `OpenSharedTestDB` を使わない。** これは `internal/db` のように接続処理そのものを試すテスト専用
- schema が分かれているので `DELETE FROM "user"` のような無条件の削除は書いてよい。ただし**それは自分の schema に閉じている前提**に依存するので、`search_path` を跨ぐ生 SQL (`public.` 明示など) を書かない
- 行の投入は**戻り値を検査する** (`require.NoError(t, db.Create(x).Error)`)。捨てると FK 違反が黙って流れ、「200 のはずが 400」のような原因から遠い症状に化ける

migration で enum を作るときは `EXCEPTION WHEN duplicate_object THEN NULL` を使う。`pg_type WHERE typname = ...` は **schema を見ない**ため、別 schema に同名の型があるだけで作成を飛ばし、直後の `CREATE TABLE` が落ちる。

## testcontainers

`internal/testutil/containers.go` が testcontainers-go で PostgreSQL 18 / Redis 7 のコンテナを起動するヘルパー (`SetupPostgres` / `SetupRedis` / `SkipIfNoDocker`) を提供する。

**PostgreSQL と Redis で使われ方がまったく違う。**

| | testcontainers | 外部サービス |
|---|---|---|
| Redis | `SetupRedis` を **25 パッケージ**が使う。`SkipIfNoDocker` で Docker が無ければ skip する | — |
| PostgreSQL | `SetupPostgres` は `internal/api/test` の **1 パッケージのみ** | `OpenTestDB` / `MustOpenTestDB` を **15 パッケージ**が使い、`TEST_DB_*` の指す PostgreSQL に直接つなぐ |

つまり **Redis は Docker があれば足りるが、PostgreSQL は自分で用意する必要がある**。`MustOpenTestDB` は失敗時に panic し、しかも `init()` から呼ばれるので、PostgreSQL が無いと該当パッケージはまとめて落ちる (skip されない)。

```go
// PostgreSQLコンテナ起動 + マイグレーション自動適用
testDB, err := testutil.SetupPostgres(ctx)
defer testDB.Teardown(ctx)

// Redisコンテナ起動
testRedis, err := testutil.SetupRedis(ctx)
defer testRedis.Teardown(ctx)

// テスト間のデータクリーンアップ
testDB.TruncateAll()
testRedis.FlushAll(ctx)
```

Docker環境がない場合は`testutil.SkipIfNoDocker(t)`でテストをスキップする。

### CI環境

CI では GitHub Actions の `services` で PostgreSQL を起動し (Redis を要するテストは CI でも testcontainers を立てる)、環境変数で接続先を指定する:

| 環境変数 | 値 |
|---|---|
| `TEST_DB_HOST` | `localhost` |
| `TEST_DB_PORT` | `5432` |
| `TEST_DB_NAME` | `misskey_test` |
| `TEST_DB_USER` | `mk` |
| `TEST_DB_PASS` | `mk` |
| `TEST_DB_SSLMODE` | `disable` |
| `TEST_REDIS_HOST` | `localhost` |
| `TEST_REDIS_PORT` | `6379` |

## テストパターン

### APIハンドラテスト (モック)

```go
func newTestHandler(t *testing.T) (*Handler, *testutil.MockUserRepository) {
    userRepo := testutil.NewMockUserRepository()
    metaRepo := testutil.NewMockMetaRepository()
    metaRepo.Meta = &model.Meta{ID: "x"}
    // モックをサービスに注入
    svc := NewService(userRepo, metaRepo)
    h := NewHandler(svc)
    return h, userRepo
}

func doPost(h func(echo.Context) error, body string, user *model.User) *httptest.ResponseRecorder {
    e := echo.New()
    req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
    req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
    rec := httptest.NewRecorder()
    c := e.NewContext(req, rec)
    if user != nil {
        c.Set(string(middleware.UserContextKey), user)
    }
    _ = h(c)
    return rec
}
```

### サービステスト (実Redis)

```go
var testRedis *testutil.TestRedis

func TestMain(m *testing.M) {
    ctx := context.Background()
    tr, err := testutil.SetupRedis(ctx)
    if err != nil {
        log.Fatalf("redis setup failed: %v", err)
    }
    testRedis = tr
    code := m.Run()
    testRedis.Teardown(ctx)
    os.Exit(code)
}

func newSvc(t *testing.T) *Service {
    t.Helper()
    testRedis.FlushAll(context.Background())
    return NewService(testutil.NewMockRepository(), testRedis.Client)
}
```

## モック一覧

`internal/testutil/`に以下のモック実装がある:

| ファイル | 内容 |
|---|---|
| `mock_repository.go` | User, Note, Following, Reaction, Meta, Role, Channel, Chat等 (~20種) |
| `mock_drive.go` | DriveFileRepository, DriveFolderRepository |
| `mock_block_mute.go` | BlockingRepository, MutingRepository, RenoteMutingRepository |
| `mock_allowlist.go` | AllowlistChecker (mediaproxy用) |
| `errors.go` | テスト用エラー定数 |

各モックはインメモリの`map[string]*Model`でデータを保持し、CRUD操作をシミュレートする。

## 連合テスト

`docker-compose.federation.misskey.yml`でmk-goとMisskey TSの2インスタンスを起動し、AP通信をテストする。

```bash
# ビルド + 起動
make federation-misskey-up

# テスト実行
make federation-misskey-test

# ログ確認
make federation-misskey-logs

# 停止
make federation-misskey-down
```

テストはPython (pytest)で記述され、`tests/federation/`に配置。両インスタンスに共通のAPI互換クライアント(`MisskeyLikeClient`)を使ってフォロー、ノート作成、リアクション等の連合動作を検証する。

## Playwright e2e (drop-in 互換 nightly)

`tests/playwright/` 配下の spec を mk-go と Misskey TS の **両 backend** で並列実行し、drop-in 互換 regression を nightly 監視する基盤。

- 範囲: 96 spec / 35 directory / 242 endpoint cover (= router.go 登録 448 endpoint の 54.3%)
- backend matrix: `mk-go` / `ts` 並列、`fail-fast: false` で片方失敗しても他方は完走
- スケジュール: nightly 17:00 UTC (`.github/workflows/playwright.yml`)
- spec は **backend-agnostic** (= URL 切替だけで両 backend で動く)、spec 失敗 = drop-in 互換 regression として issue 化

### spec を書くときの注意: root の per-user quota

UI spec は root (alice) を共有する。antenna / webhook / clip / avatar decoration には role policy の上限があり、**作りっぱなしにすると枠を使い切って無関係な spec が setup の create で落ちる**。落ちる場所が原因から離れるので診断が難しい (#2254 の調査中、実際に spec 側の bug と誤認しかけた)。

| policy | 既定値 |
|---|---|
| `antennaLimit` | 5 |
| `webhookLimit` | 3 |
| `clipLimit` | 10 |
| `avatarDecorationLimit` | 1 |

対策は 2 段構え (#2264):

- **globalSetup が run の先頭で root の quota を purge する** — 前回 run の残骸対策。同じ stack を使い回してもクリーンな状態から始まる
- **spec 自身が afterEach で片付ける** — 1 回の run の中で枠を食い潰さないため。`fixtures/quota.ts` の `deleteAntennasNamed` / `deleteWebhooksNamed` を使う

上限のあるリソースを新しく作る spec を足すときは、後者を必ず入れること。

```ts
import { deleteAntennasNamed } from '../../fixtures/quota';

const createdAntennas: string[] = [];
test.afterEach(async ({ request }) => {
  await deleteAntennasNamed(request, root.token, createdAntennas);
  createdAntennas.length = 0;
});
```

`test.afterAll` では test-scope の `request` fixture が使えないので `afterEach` を使う。

### Drift detection workflow

Playwright で発見した drift は LCD 化 → strict 化 のサイクルで消化する:

1. spec を書いて両 backend で走らせる
2. 挙動が異なる場合は `expect([200, 204]).toContain(...)` 等の **LCD (Lowest Common Denominator)** で吸収して両 backend pass させる
3. LCD のコメントで drift 内容を記録、別 issue として起票
4. drift fix PR で mk-go 側を strict 仕様 (= upstream Misskey TS の挙動) に揃える
5. 同 PR で spec の LCD を strict (`expect(...).toBe(204)` 等) に格上げ

**実績**: Phase 1-4 で 40+ 件の drift を fix。詳細は [api-compatibility.md](api-compatibility.md) の「対応済 drift fix」section、または #744 / #947 tracker 参照。

## 差分比較 e2e (値レベル)

mk-go と Misskey TS に**同一リクエストを投げてレスポンスを値レベルで diff** する
(#2078、43 比較)。守備範囲が他のゲートと違う。

| ゲート | 見ているもの |
|---|---|
| 本家 backend e2e | 本家のテストが通るか |
| shape drift | フィールドの有無・型 |
| **diff-test** | **同じ入力に対する値そのもの** |

shape が合っていても値が違う類のバグはこれでしか捕まらない。

```bash
make diff-check    # down → up → healthy 待ち → pytest を通しで
make diff-test     # スタックが既に上がっている場合
```

意図的な差分は `tests/diff/test_endpoints.py` の ignore-list に**理由付きで**登録する。
`META_IGNORE` と `USER_IGNORE` は別定義で後者は前者を継承していないので、`policies` の
ような両方に現れるキーは両方へ足す必要がある。

**ignore-list を安易に広げないこと。** 空振りさせると本物の乖離が埋もれる。追加時は
`docs/divergence.md` に対応する記述があるかを確認する。

PR ごとに `.github/workflows/diff-e2e.yml` が実行する (required check ではない)。
詳細は [diff-e2e.md](diff-e2e.md)。

## 本家 backend e2e

Misskey 本家の backend e2e (`third_party/misskey/packages/backend/test/e2e/**`) を、
**テスト本体に一切手を入れずに** mk-go へ向けて実行する。差し替えるのは vitest 設定の
2 点 (globalSetup = mk-go バイナリの起動、setupFiles = `/api/reset-db`) だけなので、
上流でテストが増えれば自動的に検証対象も増える。

```bash
make upstream-e2e-deps         # 初回 / submodule bump 後
make upstream-e2e-up           # PostgreSQL / Redis
make upstream-e2e-migrate
make upstream-e2e-test         # FILE=test/e2e/note.ts で 1 ファイルだけも可
```

『通らないことが正しい』テストは `tests/upstream-e2e/known-divergences.json` に**根拠付きで**
登録し、vitest の expected-failure (`task.fails`) として扱う。skip ではないので、乖離が
解消して通るようになったテストは逆に落ちて一覧の陳腐化に気付ける。

PR ごとに CI で実行する (`.github/workflows/upstream-backend-e2e.yml`、required check には
入れない)。詳細は [upstream-backend-e2e.md](upstream-backend-e2e.md)。

## Drop-in テスト

state preservation や frontend 視点の drop-in 互換を検証する 2 系統:

### Drop-in e2e (pytest, `tests/dropin/`)

Misskey TS 2 インスタンス (TS-A / TS-B) を起動して federation smoke を実行する基盤に、`docker-compose.dropin.mk.yml` overlay で TS-A の backend を mk-A に差し替えて **state 引き継ぎ** を検証する。

```bash
make dropin-up                 # TS-A / TS-B 起動 (smoke baseline)
make dropin-mk-up              # 上から mk-A overlay (= clean DB の mk-A)
make dropin-swap-test          # TS-then-mk 切替シナリオ (bash orchestrator)
```

PR ごとに `.github/workflows/dropin-e2e.yml` が 2 シナリオを並列実行する
(`swap-test` = `make dropin-swap-test`、`ed25519-verify` = `make dropin-fedibird-test`)。
required check には入れない。

`make dropin-fedibird-test` は Fedibird-like な AP mock を立てて **Ed25519 署名の
双方向 verify** を検証する (#1083)。Ed25519 は mk-go 独自の先行実装なので、他実装と
相互運用できるかは実際に喋らせないと分からない。ユニットテストは「自分で署名して
自分で検証する」ことしか保証しない。

### Drop-in frontend e2e (cypress, `tests/dropin_frontend/`)

3 Misskey TS インスタンス (A/B/C) + cypress runner で実ブラウザから frontend 視点の drop-in 互換を検証する。Phase 14-3 (#394) で TS-A → mk-A 切替後も spec が pass することを e2e 確認 (`CYPRESS_MODE=baseline|swap` で skip 制御)。

```bash
make dropin-frontend-baseline      # TS-A/B/C + cypress baseline spec 実行
make dropin-frontend-swap-test     # TS-A → mk-A 切替まで含む end-to-end
```

nightly 19:00 UTC で `dropin-frontend-e2e` を実行 (`.github/workflows/dropin-frontend-e2e.yml`)。
