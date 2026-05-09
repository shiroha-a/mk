# テスト

## テストの種類

| 種類 | 対象 | DB/Redis | 実行方法 |
|---|---|---|---|
| ユニットテスト | APIハンドラ、サービスロジック | モック | `go test ./internal/api/...` |
| 統合テスト | リポジトリ、Redis連携 | 実DB (testcontainers) | `go test ./internal/core/...` |
| E2Eテスト (Cypress) | フロントエンド操作 | 実DB + フロントエンド | `make e2e-run` (詳細は[E2Eテスト](e2e.md)) |
| 連合テスト | mk-go ↔ Misskey AP通信 | Docker Compose多段 | `make federation-misskey-test` |
| Drop-in e2e (pytest) | TS-A backend を mk-A に差し替えて state preservation 検証 | TS 2 instance + mk overlay | `make dropin-swap-test` (#365 / #367 / #372 / #374、詳細は[dropin-e2e.md](dropin-e2e.md)) |
| Drop-in frontend e2e (cypress) | 3 TS instance + mk overlay swap で frontend 視点の互換 | cypress + 3 TS + mk-A | `make dropin-frontend-swap-test` (#380 / #381 / #387 / #394、詳細は[dropin-frontend-e2e.md](dropin-frontend-e2e.md)) |
| Playwright e2e | mk-go と Misskey TS の両 backend で API/frontend 統合互換を nightly 監視 | Docker Compose 全部 | `tests/playwright/` 配下 (#744、Phase 1-4 完了で 96 spec / 35 directory / 242 endpoint cover = 54.3%) |

## 実行方法

```bash
# 全テスト (testcontainersでPostgreSQL/Redisが自動起動、Docker必須)
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

## testcontainers

`internal/testutil/containers.go`がtestcontainers-goでPostgreSQL 16とRedis 7のコンテナを自動起動する。ローカルにDocker環境があれば特別な準備なしでテストを実行できる。

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

CIではtestcontainersの代わりにGitHub Actionsの`services`でPostgreSQL/Redisを起動し、環境変数で接続先を指定する:

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

### Drift detection workflow

Playwright で発見した drift は LCD 化 → strict 化 のサイクルで消化する:

1. spec を書いて両 backend で走らせる
2. 挙動が異なる場合は `expect([200, 204]).toContain(...)` 等の **LCD (Lowest Common Denominator)** で吸収して両 backend pass させる
3. LCD のコメントで drift 内容を記録、別 issue として起票
4. drift fix PR で mk-go 側を strict 仕様 (= upstream Misskey TS の挙動) に揃える
5. 同 PR で spec の LCD を strict (`expect(...).toBe(204)` 等) に格上げ

**実績**: Phase 1-4 で 40+ 件の drift を fix。詳細は [api-compatibility.md](api-compatibility.md) の「対応済 drift fix」section、または #744 / #947 tracker 参照。

## Drop-in テスト

state preservation や frontend 視点の drop-in 互換を検証する 2 系統:

### Drop-in e2e (pytest, `tests/dropin/`)

Misskey TS 2 インスタンス (TS-A / TS-B) を起動して federation smoke を実行する基盤に、`docker-compose.dropin.mk.yml` overlay で TS-A の backend を mk-A に差し替えて **state 引き継ぎ** を検証する。

```bash
make dropin-up                 # TS-A / TS-B 起動 (smoke baseline)
make dropin-mk-up              # 上から mk-A overlay (= clean DB の mk-A)
make dropin-swap-test          # TS-then-mk 切替シナリオ (bash orchestrator)
```

nightly 18:00 UTC で `dropin-swap-test` を develop に対して実行 (`.github/workflows/dropin-e2e.yml`)。

### Drop-in frontend e2e (cypress, `tests/dropin_frontend/`)

3 Misskey TS インスタンス (A/B/C) + cypress runner で実ブラウザから frontend 視点の drop-in 互換を検証する。Phase 14-3 (#394) で TS-A → mk-A 切替後も spec が pass することを e2e 確認 (`CYPRESS_MODE=baseline|swap` で skip 制御)。

```bash
make dropin-frontend-baseline      # TS-A/B/C + cypress baseline spec 実行
make dropin-frontend-swap-test     # TS-A → mk-A 切替まで含む end-to-end
```

nightly 19:00 UTC で `dropin-frontend-e2e` を実行 (`.github/workflows/dropin-frontend-e2e.yml`)。
