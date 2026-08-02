.PHONY: build run dev clean tidy test fmt lint migrate-up migrate-down migrate-create \
	federation-misskey-build federation-misskey-up federation-misskey-test \
	federation-misskey-down federation-misskey-logs \
	dropin-up dropin-down dropin-test dropin-logs \
	dropin-mk-up dropin-mk-test dropin-mk-down dropin-mk-logs dropin-swap-test dropin-fedibird-test \
	dropin-frontend-up dropin-frontend-down dropin-frontend-baseline dropin-frontend-logs \
	dropin-frontend-mk-up dropin-frontend-mk-down dropin-frontend-swap-test \
	e2e-submodule-init e2e-frontend-build e2e-deps e2e-run e2e-open \
	uds-init uds-frontend-build uds-build uds-up uds-down uds-down-v uds-logs uds-ps \
	bench-up bench-run bench-down bench-logs \
	apicompat apicompat-routes apicompat-render \
	shapecheck shapecheck-gen shapecheck-report errorid-check limitspec-check perm-check \
	diff-up diff-test diff-down diff-logs

# Binary output
BINARY=misskey
BUILD_DIR=./built

# Go parameters
GOFLAGS=-trimpath

# バージョン情報。MisskeyVersion は /api/meta の version フィールド
# (Misskey TS 互換クライアント向け) で使われる。
#
# 既定では -X を付けず `internal/config` の定数をそのまま使う。以前はここに
# 版数をハードコードしていたが、追従のたびに更新が漏れて `make build` が
# 2 リリース前の版数を報告する状態になっていた (Dockerfile は -X を付けない
# ので本番は無事だったが、`make build` 産のバイナリと route dump が古い版数を
# 名乗っていた)。定数を唯一の source of truth にして二重管理をやめる。
#
# リリースビルドで上書きしたい場合のみ変数を渡す:
#   make build MKGO_VERSION=1.0.0
MKGO_VERSION ?=
MISSKEY_VERSION ?=
LDFLAGS=-s -w
ifneq ($(MKGO_VERSION),)
LDFLAGS += -X github.com/shiroha-a/mk/internal/config.MkGoVersion=$(MKGO_VERSION)
endif
ifneq ($(MISSKEY_VERSION),)
LDFLAGS += -X github.com/shiroha-a/mk/internal/config.MisskeyVersion=$(MISSKEY_VERSION)
endif

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/misskey

run: build
	$(BUILD_DIR)/$(BINARY) -config .config/default.yml

dev:
	go run ./cmd/misskey -config .config/default.yml

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy

test:
	go test ./... -v

fmt:
	gofmt -s -w .

lint:
	go vet ./...

# Migration (requires DATABASE_URL env var)
migrate-up:
	go run ./cmd/migrate -direction up

migrate-down:
	go run ./cmd/migrate -direction down

migrate-create:
	@read -p "Migration name: " name; \
	touch migration/$$(printf "%06d" $$(($$(ls migration/*.up.sql 2>/dev/null | wc -l) + 1)))_$${name}.up.sql; \
	touch migration/$$(printf "%06d" $$(($$(ls migration/*.down.sql 2>/dev/null | wc -l) + 1)))_$${name}.down.sql

# Docker
docker-build:
	docker build -t mk-go .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

# Federation tests ― 本家 Misskey と実際に立ち上げて連合動作を検証する。
# 各ターゲット (misskey / mastodon / pleroma / ...) ごとに docker-compose.federation.<target>.yml を用意する。
FEDERATION_MISSKEY_COMPOSE=docker-compose.federation.misskey.yml

federation-misskey-build:
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) build

federation-misskey-up:
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) up -d --build

federation-misskey-test:
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) --profile test run --rm test-runner

federation-misskey-down:
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) down -v

federation-misskey-logs:
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) logs -f

# Drop-in e2e (#365) ― Misskey TS 2 インスタンス (A, B) を立ち上げて
# 連合基盤を検証する。Phase 13-1 では TS ↔ TS の smoke test のみ。
# Phase 13-2 以降で mk 差し替え overlay を追加する予定。
DROPIN_COMPOSE=docker-compose.dropin.yml

dropin-up:
	docker compose -f $(DROPIN_COMPOSE) up -d --build

dropin-test:
	docker compose -f $(DROPIN_COMPOSE) --profile test run --rm test-runner

dropin-down:
	docker compose -f $(DROPIN_COMPOSE) down -v

dropin-logs:
	docker compose -f $(DROPIN_COMPOSE) logs -f

# Drop-in mk overlay (#367) — instance A の backend を mk-go に差し替えた
# 状態で TS-A 用 stack を起動する。連合先 (instance B) は TS のままなので
# mk ↔ TS federation も同時に検証できる。
DROPIN_MK_OVERLAY=docker-compose.dropin.mk.yml

dropin-mk-up:
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) up -d --build

dropin-mk-test:
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) --profile test run --rm test-runner

dropin-mk-down:
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) down -v

dropin-mk-logs:
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) logs -f

# Drop-in swap シナリオ (#367): TS-A → mk-A 切替で state が引き継げることを
# 検証する end-to-end テスト。bash orchestrator が以下を順次実行する:
#   1. TS-A + TS-B 起動
#   2. test_swap_setup.py で alice/bob/follow/note を作る
#   3. TS-A backend を停止
#   4. overlay で mk-A 起動 (DB-A / Redis-A はそのまま)
#   5. test_swap_verify.py で state preserved + 新規 federation を確認
dropin-swap-test:
	./tests/dropin/run-swap-test.sh

# Drop-in fedibird-mock e2e (#1083) — base + mk + fedibird overlay の stack で
# Fedibird-like ActivityPub mock を立てて、mk-A との Ed25519 双方向 verify を
# walks through する。ed25519 P2-P5 が実 federation 経路で動くことを担保する
# nightly 用 e2e。
dropin-fedibird-test:
	./tests/dropin/run-fedibird-test.sh

# Drop-in frontend e2e (#380 / Phase 14) ― 3 Misskey TS インスタンス上で
# cypress を回して、共有 TS フロントエンドから観測可能なアクティビティの
# 整合性を検証する基盤。Phase 14-1 は baseline (all TS) のみ。
DROPIN_FRONTEND_COMPOSE=docker-compose.dropin-frontend.yml

dropin-frontend-up:
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) up -d

dropin-frontend-down:
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) down -v

dropin-frontend-logs:
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) logs -f

# baseline: all TS な状態で cypress spec が全 pass することを確認する
# (Phase 14-1 #381)。
dropin-frontend-baseline:
	./tests/dropin_frontend/run-frontend-baseline.sh

# Phase 14-3 (#394): TS-A → mk-A 切替後も cypress spec が引き続き pass する
# ことを確認する swap test orchestrator。baseline 実行 → TS-A 停止 → mk-A
# 起動 → swap モードで cypress 再実行、を bash で順次制御する。
DROPIN_FRONTEND_MK_OVERLAY=docker-compose.dropin-frontend.mk.yml

# mk-go overlay を直接立ち上げる (手動デバッグ用)。DB は clean からだが、
# Phase 14-3 の本 test は `dropin-frontend-swap-test` を使う。
dropin-frontend-mk-up:
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) -f $(DROPIN_FRONTEND_MK_OVERLAY) up -d --build

dropin-frontend-mk-down:
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) -f $(DROPIN_FRONTEND_MK_OVERLAY) down -v

dropin-frontend-swap-test:
	./tests/dropin_frontend/run-frontend-swap-test.sh

# Cypress e2e ― Misskey 本家の cypress spec を mk-go に向けて実行する。
#
# ライセンス境界のため、本家コードはすべて third_party/misskey/ の git submodule
# 参照で扱う。mk-go のリポジトリには 1 行もコピーしない。
#
# CLAUDE.md の規約で「パッケージはホストに直接入れずコンテナ経由で動かす」と
# 決まっているため、pnpm / cypress はすべて docker run で実行する。
E2E_NODE_IMAGE=node:22-bookworm
E2E_CYPRESS_IMAGE=cypress/included:15.11.0
E2E_WORKDIR=/work

# submodule を初期化し、Misskey 本家の cypress 資産とフロントエンドソースを取得する。
e2e-submodule-init:
	git submodule update --init --recursive third_party/misskey

# 本家フロントエンドを Docker 内でビルドする。数分〜10 分程度かかる。
# 成果物は third_party/misskey/packages/frontend/... 配下に出力される。
# パッチは submodule (shiroha-a/misskey-ts、tag 2026.5.4-mk.0) に直接コミット済み。
#
# CI=true を渡す理由: upstream 2026.5.2 で pnpm 10 → 11 に移行 (#17400 dep bump
# 系)、pnpm 11 は previous install (node_modules) を消す前に prompt を出す挙動が
# default。docker run は TTY 無し起動なので prompt が出せず ERR_PNPM_ABORTED_
# REMOVE_MODULES_DIR_NO_TTY で abort する。CI=true で skip させる。
e2e-frontend-build:
	docker run --rm -e CI=true -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/third_party/misskey \
		$(E2E_NODE_IMAGE) \
		bash -lc "corepack enable && corepack prepare pnpm@latest --activate && pnpm install --frozen-lockfile && pnpm build"

# Cypress ラッパーの依存を Docker 内で解決する。
e2e-deps:
	docker run --rm -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/e2e/cypress \
		$(E2E_NODE_IMAGE) \
		bash -lc "corepack enable && corepack prepare pnpm@latest --activate && pnpm install"

# ヘッドレスで cypress run を実行する。
# host network で動かして mk-go (localhost:3000) に直接届かせる。
e2e-run:
	docker run --rm --network=host -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/e2e/cypress \
		-e E2E_BASE_URL=$${E2E_BASE_URL:-http://localhost:3000} \
		$(E2E_CYPRESS_IMAGE) \
		cypress run --e2e --browser electron --config-file cypress.config.ts

# 開発者向けに cypress open を起動する (X11 forward が必要なので CI では使わない)。
e2e-open:
	docker run --rm --network=host -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/e2e/cypress \
		-e DISPLAY=$$DISPLAY -v /tmp/.X11-unix:/tmp/.X11-unix \
		-e E2E_BASE_URL=$${E2E_BASE_URL:-http://localhost:3000} \
		$(E2E_CYPRESS_IMAGE) \
		cypress open --e2e --browser electron --config-file cypress.config.ts

# UDS-only compose stack (Phase 12-2)。Phase 12-1 で入った UNIX domain socket
# 対応を使って nginx → mk-go → postgres / valkey をすべて UDS で繋ぐ。
# 詳細は docs/docker-uds.md を参照。
UDS_COMPOSE=compose.uds.yaml
UDS_CONFIG=deploy/uds/config/default.yml

# compose / config の実ファイルはデプロイ先ごとに書き換えるため gitignore 済み。
# 初回起動時のみ .example からコピーする (order-only prerequisite なので、
# 一度作成したあとは .example を更新してもユーザのローカル編集を上書きしない)。
$(UDS_COMPOSE):
	cp $(UDS_COMPOSE).example $(UDS_COMPOSE)

$(UDS_CONFIG):
	cp $(UDS_CONFIG).example $(UDS_CONFIG)

uds-init: | $(UDS_COMPOSE) $(UDS_CONFIG)

# 本家 vite フロントエンドを docker 内でビルドする。初回は 3〜10 分程度かかる。
# 既存 e2e-frontend-build のエイリアス (成果物先が同じなので共有して OK)。
uds-frontend-build: e2e-frontend-build

uds-build: | $(UDS_COMPOSE) $(UDS_CONFIG)
	docker compose -f $(UDS_COMPOSE) build

uds-up: | $(UDS_COMPOSE) $(UDS_CONFIG)
	docker compose -f $(UDS_COMPOSE) up -d --build

uds-down: | $(UDS_COMPOSE)
	docker compose -f $(UDS_COMPOSE) down

# named volume も含めて完全削除する (DB データも全部消える)。
uds-down-v: | $(UDS_COMPOSE)
	docker compose -f $(UDS_COMPOSE) down -v

uds-logs: | $(UDS_COMPOSE)
	docker compose -f $(UDS_COMPOSE) logs -f

uds-ps: | $(UDS_COMPOSE)
	docker compose -f $(UDS_COMPOSE) ps

# Benchmark ― mk-go vs 本家 Misskey のストレステスト比較。
# k6 (Docker) で同一エンドポイントに負荷をかけ、レイテンシ・スループットを比較する。
# 結果は tests/bench/results/report.md に出力される。
BENCH_COMPOSE=tests/bench/docker-compose.bench.yml

bench-up:
	docker compose -f $(BENCH_COMPOSE) up -d --build

bench-run:
	docker compose -f $(BENCH_COMPOSE) --profile bench up --abort-on-container-exit compare

bench-down:
	docker compose -f $(BENCH_COMPOSE) down -v

bench-logs:
	docker compose -f $(BENCH_COMPOSE) logs -f

# Queue bench (#563): 3-way deliver/inbox throughput comparison across
# Misskey TS (BullMQ), mk-go (asynq), mk-go (mkq).
QUEUE_BENCH_COMPOSE=tests/queue-bench/docker-compose.queue-bench.yml

queue-bench-up:
	docker compose -f $(QUEUE_BENCH_COMPOSE) up -d --build

queue-bench-seed:
	# `--force-recreate` で seed container を毎回 fresh に作る (#1163)。
	# down → up を繰り返すと network が再作成されて新 ID になるが、profile
	# container は queue-bench-down (= `down -v`) の対象外で残る。古い container
	# は attach 先の network ID が変わったまま固定されて、次回 start 時に
	# `network <hash> not found` で失敗する非決定性を引き起こすため、毎回
	# 強制的に再作成する。同じ理由を queue-bench-outbound / -inbound / -report
	# にも適用している。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile bench up --abort-on-container-exit --force-recreate seed
	# meta cache (5min TTL) が古い federation='none' を握っているので、seed
	# 後に app コンテナを再起動して新しい meta.federation='all' を読ませる。
	docker compose -f $(QUEUE_BENCH_COMPOSE) restart app-asynq app-mkq app-ts
	@echo "waiting for apps to become healthy after restart..."
	@for i in $$(seq 1 60); do \
		ASYNQ=$$(docker compose -f $(QUEUE_BENCH_COMPOSE) ps app-asynq --format json 2>/dev/null | grep -o '"Health":"healthy"' || true); \
		MKQ=$$(docker compose -f $(QUEUE_BENCH_COMPOSE) ps app-mkq --format json 2>/dev/null | grep -o '"Health":"healthy"' || true); \
		TS=$$(docker compose -f $(QUEUE_BENCH_COMPOSE) ps app-ts --format json 2>/dev/null | grep -o '"Health":"healthy"' || true); \
		if [ -n "$$ASYNQ" ] && [ -n "$$MKQ" ] && [ -n "$$TS" ]; then \
			echo "ready (asynq+mkq+ts all healthy)"; exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "warning: not all apps became healthy in time" >&2; exit 1

queue-bench-outbound:
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile outbound up --abort-on-container-exit --force-recreate driver-outbound

queue-bench-inbound:
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile inbound up --abort-on-container-exit --force-recreate driver-inbound

queue-bench-report:
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile report up --abort-on-container-exit --force-recreate report

queue-bench-all: queue-bench-seed queue-bench-outbound queue-bench-inbound queue-bench-report

queue-bench-down:
	# `--remove-orphans` で profile container も含めて確実に cleanup する (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) down -v --remove-orphans

queue-bench-logs:
	docker compose -f $(QUEUE_BENCH_COMPOSE) logs -f

# Auto-scale comparison bench (#1126 / #1120 tracker).
# 3 scenario (fixed16 / fixed64 / auto) を同一 mkq stack で逐次実行し、
# drain time / Redis client count を比較する。queue-bench との同居・
# 並列実行は想定しない (port は publish していないが volume / network 名は
# 別)。詳細: tests/queue-bench-autoscale/README.md (or docs/queue-bench.md)
AUTOSCALE_BENCH_DIR=tests/queue-bench-autoscale

queue-bench-autoscale-run:
	cd $(AUTOSCALE_BENCH_DIR) && ./run.sh

queue-bench-autoscale-down:
	cd $(AUTOSCALE_BENCH_DIR) && docker compose down -v --remove-orphans

queue-bench-autoscale-logs:
	cd $(AUTOSCALE_BENCH_DIR) && docker compose logs -f

# Playwright e2e (#744 Phase 1)
#
# upstream Misskey TS 互換挙動を期待値に書いた spec を mk-go backend に
# 対して走らせ、drop-in 互換 regression を検出する。Phase 1 PR-1 では
# 基盤 + smoke 1 spec のみ。後続 PR で spec 拡充 + CI 統合する。
PLAYWRIGHT_COMPOSE=docker-compose.playwright.yml

playwright-up:
	docker compose -f $(PLAYWRIGHT_COMPOSE) up -d --build

playwright-test:
	# `--build` を付けて runner image を rebuild check させる。package.json
	# 更新時に node_modules が古いままにならないよう、毎回 build context を
	# 確認する (cache hit なら ms 単位で済むので overhead 無視可)。
	docker compose -f $(PLAYWRIGHT_COMPOSE) --profile test run --rm --build playwright-runner

playwright-down:
	docker compose -f $(PLAYWRIGHT_COMPOSE) down -v

playwright-logs:
	docker compose -f $(PLAYWRIGHT_COMPOSE) logs -f

# Playwright TS validation (#744 Phase 1)
#
# 同 spec を upstream Misskey TS image (= 真の互換挙動の baseline) に対しても
# 走らせる。両方で pass = drop-in 互換が確認される、片方のみ pass = drift /
# spec 誤りとして調査対象。
PLAYWRIGHT_TS_OVERLAY=docker-compose.playwright.ts.yml

playwright-ts-up:
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) up -d --build

playwright-ts-test:
	# `playwright-test` と同じく `--build` で runner image を最新化する。
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) --profile test run --rm --build playwright-runner

playwright-ts-down:
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) down -v

# Differential e2e diff harness (#2089) ― mk-go (2026.7.0) と Misskey TS
# (2026.5.4) を並列に立て、同一 endpoint のレスポンスを diff して entitycompat
# golden gate がカバーしない値レベル乖離を検出する。詳細は docs/diff-e2e.md。
# 隔離 stack (own network/volumes)、production UDS には触れない。
DIFF_COMPOSE=docker-compose.diff.yml

diff-up:
	docker compose -f $(DIFF_COMPOSE) up -d --build

diff-test:
	docker compose -f $(DIFF_COMPOSE) --profile test run --rm --build diff-runner

diff-down:
	docker compose -f $(DIFF_COMPOSE) down -v

diff-logs:
	docker compose -f $(DIFF_COMPOSE) logs -f

# API compatibility matrix ― mk-go と Misskey TS の API endpoint 実装状況を
# 突き合わせて docs/api-compat.md を生成する。
#
# - APICOMPAT_TS_DIR: TS endpoints ディレクトリ。submodule に依存。
# - APICOMPAT_CONFIG: --dump-routes 時に読み込む mk-go config。DB/Redis 接続
#   は必須なので、docker compose up された stack を持っていることが前提。
# - APICOMPAT_ROUTES: dump-routes が書き出す中間ファイルの path。
#   `$(BUILD_DIR)` 配下にして hermetic に保つ ( /tmp 共有事故を避ける)。
APICOMPAT_TS_DIR    ?= third_party/misskey/packages/backend/src/server/api/endpoints
# fastify 直登録 endpoint (signup / signin-flow / miauth check / instance peers)
# の抽出元。endpoints/ の file-walk では拾えないので source から直接読む。
APICOMPAT_TS_DIRECT ?= third_party/misskey/packages/backend/src/server/api/ApiServerService.ts
APICOMPAT_CONFIG    ?= .config/default.yml
APICOMPAT_ROUTES    ?= $(BUILD_DIR)/apicompat-routes.json
APICOMPAT_OUT       ?= docs/api-compat.md

# mk-go binary を build → --dump-routes で route 一覧を JSON dump。
# DB / Redis 接続を必要とするので make docker-up 等で stack を立てた状態で
# 実行すること。
apicompat-routes: build
	mkdir -p $(dir $(APICOMPAT_ROUTES))
	$(BUILD_DIR)/$(BINARY) -config $(APICOMPAT_CONFIG) -dump-routes -dump-routes-out $(APICOMPAT_ROUTES)

# 既存 APICOMPAT_ROUTES JSON だけ comparator にかけて matrix を再生成する
# (DB / Redis 接続不要)。matrix の format / category 表示を iterate する時に
# 毎回 build + dump し直さなくて済むよう用意した escape hatch。前提として
# `apicompat-routes` を最低一度走らせていること。
apicompat-render:
	go run ./tools/apicompat \
		-ts-endpoints-dir $(APICOMPAT_TS_DIR) \
		-ts-api-server-service $(APICOMPAT_TS_DIRECT) \
		-mk-routes $(APICOMPAT_ROUTES) \
		-out $(APICOMPAT_OUT)
	@echo "wrote $(APICOMPAT_OUT)"

# routes JSON + TS endpoints ディレクトリを comparator で突き合わせて matrix
# を生成する。`apicompat-routes` を都度先に走らせて、stale な中間 JSON で
# matrix を作らないようにする (`.PHONY` 効果で常に build + dump し直す)。
# 反復 iterate で DB 接続を毎回避けたい場合は `apicompat-render` を使う。
apicompat: apicompat-routes apicompat-render

# --- entity shape drift (Layer 0 static API compatibility) ------------------
# mk-go の entity DTO struct を Misskey contract (misskey-js types.ts) と
# フィールド単位で突き合わせ、shape drift (欠落 / null性 / optional性) を検出する。
# サーバー / ブラウザ / Docker 不要で、CI では `go test ./...` 内の
# TestEntityShapeDrift gate として自動実行される。詳細は docs/shape-drift.md。

# golden snapshot (testdata/golden_schemas.json + golden_error_ids.json) を
# submodule から再生成する。third_party/misskey を upstream catch-up で bump
# したら必ず実行し、生成された snapshot を commit すること。
shapecheck-gen:
	go run ./tools/shapediff
	go run ./tools/erroriddiff
	go run ./tools/limitspec
	go run ./tools/permspec
	go run ./tools/securespec

# 全 family の drift を severity 付きで一覧表示する (gate にかける前の調査用)。
shapecheck-report:
	go run ./tools/shapediff -report

# drift gate (L0 静的 + L2 実行時) をローカルで実行する (CI と同じ判定)。
# L0: TestEntityShapeDrift / L2: Test*ShapeL2 (Notification / Announcement / ...)。
shapecheck:
	go test ./internal/entitycompat/... -run 'TestEntityShapeDrift|ShapeL2' -count=1 -v

# error-id / error-HTTP-status / error-kind drift gate をローカルで実行する。
# handler が emit する error id (inline / UUID 定数 / apierr helper / echo
# wrapper) と、Misskey が明示する HTTP status / kind discriminator を router
# 経由で endpoint に解決して突合する静的 gate。詳細は docs/shape-drift.md。
errorid-check:
	go test ./internal/entitycompat/... -run 'TestErrorIDDrift|TestErrorHTTPStatusDrift|TestErrorKindDrift' -count=1 -v

# pagination limit-spec drift gate をローカルで実行する。handler が
# pagination.ClampLimit(limit, def, max) で渡す default/max literal を router
# 経由で endpoint に解決し、Misskey paramDef の default/maximum と突合する。
limitspec-check:
	go test ./internal/entitycompat/... -run TestLimitSpecDrift -count=1 -v

# permission drift gate をローカルで実行する。mk-go の router middleware が
# Misskey の requireAdmin/requireModerator/requireCredential より緩くないか検証。
perm-check:
	go test ./internal/entitycompat/... -run 'TestPermissionDrift|TestSecureDrift' -count=1 -v
