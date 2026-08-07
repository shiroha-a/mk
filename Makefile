.PHONY: help check gates version frontend-check diff-check playwright-check e2e-down-all \
	update docker-update uds-update \
	image-up image-down image-down-v image-logs image-build \
	build run dev clean tidy test fmt lint migrate-up migrate-down migrate-create \
	federation-misskey-build federation-misskey-up federation-misskey-test \
	federation-misskey-e2e \
	federation-misskey-down federation-misskey-logs \
	dropin-up dropin-down dropin-test dropin-logs \
	dropin-mk-up dropin-mk-test dropin-mk-down dropin-mk-logs dropin-swap-test dropin-fedibird-test \
	dropin-mkgo-born-test \
	dropin-frontend-up dropin-frontend-down dropin-frontend-baseline dropin-frontend-logs \
	dropin-frontend-mk-up dropin-frontend-mk-down dropin-frontend-swap-test \
	e2e-submodule-init e2e-frontend-build e2e-deps e2e-run e2e-open \
	uds-init uds-frontend-build uds-build uds-up uds-down uds-down-v uds-logs uds-ps \
	bench-up bench-run bench-down bench-logs \
	apicompat apicompat-routes apicompat-render \
	shapecheck shapecheck-gen shapecheck-report errorid-check limitspec-check perm-check \
	diff-up diff-test diff-down diff-logs \
	upstream-e2e upstream-e2e-deps upstream-e2e-up upstream-e2e-down upstream-e2e-migrate upstream-e2e-test

.DEFAULT_GOAL := help

##@ ヘルプ

help: ## この一覧を表示 (引数なしの make でも出る)
	@awk 'BEGIN {FS = ":.*##"} \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_.-]+:.*##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\n各 e2e / ベンチの詳細は docs/development.md を参照。\n"

##@ まとめて実行

check: fmt lint test ## コミット前の必須 3 点 (fmt → lint → test)

gates: shapecheck errorid-check limitspec-check perm-check ## 静的 parity ゲートを一括実行

version: ## mk-go / 互換 Misskey / submodule のバージョンを表示
	@printf "mk-go            : %s\n" "$$(sed -n 's/^var MkGoVersion = "\(.*\)"/\1/p' internal/config/config.go)"
	@printf "互換 Misskey     : %s\n" "$$(sed -n 's/^var MisskeyVersion = "\(.*\)"/\1/p' internal/config/config.go)"
	@printf "submodule (fork) : %s\n" "$$(git -C third_party/misskey describe --tags 2>/dev/null || echo '(未取得)')"

frontend-check: ## fork の frontend を型チェック (vue-tsc + eslint)
	# uds-frontend-build / e2e-frontend-build は本番が bind-mount している
	# third_party/misskey/built を書き換えるため、検証目的では使わないこと。
	# 型を見るだけならこちらで済む (Docker 不要、出力物も作らない)。
	cd third_party/misskey/packages/frontend && npx vue-tsc --noEmit

diff-check: ## 差分比較ハーネスを作り直して実行 (クリーン DB 前提)
	$(MAKE) diff-down
	$(MAKE) diff-up
	@printf "backend の healthy 待ち...\n"
	@until [ "$$(docker inspect mkdiff-mkgo-1 --format '{{.State.Health.Status}}' 2>/dev/null)" = healthy ] \
		&& [ "$$(docker inspect mkdiff-ts-1 --format '{{.State.Health.Status}}' 2>/dev/null)" = healthy ]; do sleep 3; done
	$(MAKE) diff-test

playwright-check: ## Playwright を作り直して実行 (クリーン DB 前提)
	$(MAKE) playwright-down
	$(MAKE) playwright-up
	@printf "backend の healthy 待ち...\n"
	@until [ "$$(docker inspect mk-playwright-mkgo-1 --format '{{.State.Health.Status}}' 2>/dev/null)" = healthy ]; do sleep 3; done
	$(MAKE) playwright-test

# 検証用スタックだけを撤去する。**docker-compose.yml は含めない** —
# あちらは name: を持たないため project 名がディレクトリ名 (= mk) になり、
# 同名で動いている本番スタックを巻き込む。ここでは name: を明示している
# 検証用 compose ファイルだけを列挙する。
E2E_COMPOSE_FILES = \
	docker-compose.image.yml \
	docker-compose.diff.yml \
	docker-compose.playwright.yml \
	docker-compose.dropin.yml \
	docker-compose.dropin-frontend.yml \
	docker-compose.federation.misskey.yml \
	tests/bench/docker-compose.bench.yml \
	tests/queue-bench/docker-compose.queue-bench.yml

e2e-down-all: ## 検証用スタックを一括撤去 (本番 project mk は対象外)
	@for f in $(E2E_COMPOSE_FILES); do \
		printf "==> %s\n" "$$f"; \
		docker compose -f "$$f" down -v --remove-orphans 2>&1 | tail -1 || true; \
	done

##@ 更新 (運用)

update: ## submodule ごと pull し、frontend 再ビルドの要否を知らせる
	@before=$$(git -C third_party/misskey rev-parse HEAD 2>/dev/null); \
	git pull --recurse-submodules; \
	after=$$(git -C third_party/misskey rev-parse HEAD 2>/dev/null); \
	if [ "$$before" != "$$after" ]; then \
		printf "\n\033[33m==> submodule が更新された。frontend の再ビルドが必要\033[0m\n"; \
		printf "    make docker-update   (Docker Compose 構成)\n"; \
		printf "    make uds-update      (UDS 本番構成)\n"; \
	else \
		printf "\n==> submodule に変更なし。frontend の再ビルドは不要\n"; \
	fi

# frontend の再ビルドと再起動は必ずセットで行う。mk-go は entry point を
# 起動時に 1 回だけ解決してキャッシュするため、ビルドだけして再起動しないと
# HTML が消えた古い scripts/<hash>.js を指したまま 404 になる。
docker-update: ## pull → frontend ビルド → image 再ビルド → 再起動 (Docker Compose 構成)
	$(MAKE) update
	$(MAKE) e2e-frontend-build
	docker compose build
	docker compose up -d

uds-update: ## pull → frontend ビルド → image 再ビルド → 再起動 (UDS 本番構成)
	$(MAKE) update
	$(MAKE) uds-frontend-build
	$(MAKE) uds-build
	$(MAKE) uds-up


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
#   make build MKGO_VERSION=1.1.1
MKGO_VERSION ?=
MISSKEY_VERSION ?=
LDFLAGS=-s -w
ifneq ($(MKGO_VERSION),)
LDFLAGS += -X github.com/shiroha-a/mk/internal/config.MkGoVersion=$(MKGO_VERSION)
endif
ifneq ($(MISSKEY_VERSION),)
LDFLAGS += -X github.com/shiroha-a/mk/internal/config.MisskeyVersion=$(MISSKEY_VERSION)
endif

##@ 開発
build: ## バイナリを ./built/misskey に生成
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/misskey

run: build ## build して起動
	$(BUILD_DIR)/$(BINARY) -config .config/default.yml

dev: ## go run で直接起動
	go run ./cmd/misskey -config .config/default.yml

clean: ## ビルド成果物を削除
	rm -rf $(BUILD_DIR)

tidy: ## go mod tidy
	go mod tidy

test: ## 全テストを実行
	go test ./... -v

fmt: ## gofmt -s -w . で整形
	gofmt -s -w .

lint: ## go vet ./...
	go vet ./...

# Migration (requires DATABASE_URL env var)
##@ マイグレーション
migrate-up: ## マイグレーションを最新まで適用 (DATABASE_URL 必要)
	go run ./cmd/migrate -direction up

migrate-down: ## マイグレーションを 1 段階ロールバック
	go run ./cmd/migrate -direction down

migrate-create: ## 新規マイグレーションファイルを作成
	@read -p "Migration name: " name; \
	touch migration/$$(printf "%06d" $$(($$(ls migration/*.up.sql 2>/dev/null | wc -l) + 1)))_$${name}.up.sql; \
	touch migration/$$(printf "%06d" $$(($$(ls migration/*.down.sql 2>/dev/null | wc -l) + 1)))_$${name}.down.sql

# Docker
##@ Docker
docker-build: ## Docker イメージをビルド
	docker build -t mk-go .

docker-up: ## docker compose up -d
	docker compose up -d

docker-down: ## docker compose down
	docker compose down

##@ pull して起動 (ビルド不要)

# publish 済みの bundled image を pull して動かす。フロントエンドのビルドも
# image のビルドも不要。既存の docker-compose.yml / make docker-* はそのまま
# 使えるので、こちらは置き換えではなく並立する選択肢。
IMAGE_COMPOSE=docker-compose.image.yml

image-up: ## bundled image を pull して起動
	docker compose -f $(IMAGE_COMPOSE) up -d

image-down: ## 上記スタックを撤去
	docker compose -f $(IMAGE_COMPOSE) down

image-down-v: ## 上記スタックを volume ごと撤去
	docker compose -f $(IMAGE_COMPOSE) down -v

image-logs: ## 上記スタックのログを表示
	docker compose -f $(IMAGE_COMPOSE) logs -f

image-build: ## bundled image を手元でビルドする (publish 前の確認用)
	docker build -f Dockerfile.bundled -t ghcr.io/shiroha-a/mk:bundled .


# Federation tests ― 本家 Misskey と実際に立ち上げて連合動作を検証する。
# 各ターゲット (misskey / mastodon / pleroma / ...) ごとに docker-compose.federation.<target>.yml を用意する。
FEDERATION_MISSKEY_COMPOSE=docker-compose.federation.misskey.yml

##@ e2e: 連合
federation-misskey-build: ## 連合テスト用 Misskey イメージをビルド
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) build

federation-misskey-up: ## 連合テスト用 Misskey インスタンスを起動
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) up -d --build

federation-misskey-test: ## 連合テストを実行
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) --profile test run --rm test-runner

# 起動 → healthy 待ち → pytest → 撤去 を 1 コマンドで通す。CI から呼ぶのはこれ。
# 個別の up / test を手で叩くのと違い、失敗しても trap で必ず後始末する。
federation-misskey-e2e: ## 連合テストを起動から撤去まで通しで実行
	./tests/federation/run-misskey-test.sh

federation-misskey-down: ## 連合テストスタックを撤去
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) down -v

federation-misskey-logs: ## 連合テストスタックのログを表示
	docker compose -f $(FEDERATION_MISSKEY_COMPOSE) logs -f

# Drop-in e2e (#365) ― Misskey TS 2 インスタンス (A, B) を立ち上げて
# 連合基盤を検証する。Phase 13-1 では TS ↔ TS の smoke test のみ。
# Phase 13-2 以降で mk 差し替え overlay を追加する予定。
DROPIN_COMPOSE=docker-compose.dropin.yml

##@ e2e: drop-in 互換
dropin-up: ## drop-in e2e スタック (TS 2 インスタンス) を起動
	docker compose -f $(DROPIN_COMPOSE) up -d --build

dropin-test: ## drop-in e2e の smoke test を実行
	docker compose -f $(DROPIN_COMPOSE) --profile test run --rm test-runner

dropin-down: ## drop-in e2e スタックを撤去
	docker compose -f $(DROPIN_COMPOSE) down -v

dropin-logs: ## drop-in e2e スタックのログを表示
	docker compose -f $(DROPIN_COMPOSE) logs -f

# Drop-in mk overlay (#367) — instance A の backend を mk-go に差し替えた
# 状態で TS-A 用 stack を起動する。連合先 (instance B) は TS のままなので
# mk ↔ TS federation も同時に検証できる。
DROPIN_MK_OVERLAY=docker-compose.dropin.mk.yml

dropin-mk-up: ## drop-in e2e に mk-go overlay を適用して起動
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) up -d --build

dropin-mk-test: ## mk-go overlay に対する smoke test を実行
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) --profile test run --rm test-runner

dropin-mk-down: ## mk-go overlay を撤去
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) down -v

dropin-mk-logs: ## mk-go overlay のログを表示
	docker compose -f $(DROPIN_COMPOSE) -f $(DROPIN_MK_OVERLAY) logs -f

# Drop-in swap シナリオ (#367): TS-A → mk-A 切替で state が引き継げることを
# 検証する end-to-end テスト。bash orchestrator が以下を順次実行する:
#   1. TS-A + TS-B 起動
#   2. test_swap_setup.py で alice/bob/follow/note を作る
#   3. TS-A backend を停止
#   4. overlay で mk-A 起動 (DB-A / Redis-A はそのまま)
#   5. test_swap_verify.py で state preserved + 新規 federation を確認
dropin-swap-test: ## TS → mk-go 切替の state preservation を通しで検証
	./tests/dropin/run-swap-test.sh

# Drop-in fedibird-mock e2e (#1083) — base + mk + fedibird overlay の stack で
# Fedibird-like ActivityPub mock を立てて、mk-A との Ed25519 双方向 verify を
# walks through する。ed25519 P2-P5 が実 federation 経路で動くことを担保する
# nightly 用 e2e。
# mk-go 生まれの DB を TS に引き渡す経路 (#2379)。swap test (TS→mk-go→TS) とは
# 別物で、TS が一度も触っていない schema を受け取る。運用上はロックインの有無
# そのもの (mk-go で始めた人が Misskey に移れるか)。
dropin-mkgo-born-test: ## mk-go 生まれの DB を TS に引き渡せるか検証
	./tests/dropin/run-mkgo-born-test.sh

dropin-fedibird-test: ## Fedibird-like AP mock との Ed25519 双方向 verify
	./tests/dropin/run-fedibird-test.sh

# Drop-in frontend e2e (#380 / Phase 14) ― 3 Misskey TS インスタンス上で
# cypress を回して、共有 TS フロントエンドから観測可能なアクティビティの
# 整合性を検証する基盤。Phase 14-1 は baseline (all TS) のみ。
DROPIN_FRONTEND_COMPOSE=docker-compose.dropin-frontend.yml

dropin-frontend-up: ## drop-in frontend e2e スタックを起動
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) up -d

dropin-frontend-down: ## drop-in frontend e2e スタックを撤去
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) down -v

dropin-frontend-logs: ## drop-in frontend e2e のログを表示
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) logs -f

# baseline: all TS な状態で cypress spec が全 pass することを確認する
# (Phase 14-1 #381)。
dropin-frontend-baseline: ## 3 TS インスタンス + cypress で baseline spec を実行
	./tests/dropin_frontend/run-frontend-baseline.sh

# Phase 14-3 (#394): TS-A → mk-A 切替後も cypress spec が引き続き pass する
# ことを確認する swap test orchestrator。baseline 実行 → TS-A 停止 → mk-A
# 起動 → swap モードで cypress 再実行、を bash で順次制御する。
DROPIN_FRONTEND_MK_OVERLAY=docker-compose.dropin-frontend.mk.yml

# mk-go overlay を直接立ち上げる (手動デバッグ用)。DB は clean からだが、
# Phase 14-3 の本 test は `dropin-frontend-swap-test` を使う。
dropin-frontend-mk-up: ## drop-in frontend e2e に mk overlay を適用して起動
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) -f $(DROPIN_FRONTEND_MK_OVERLAY) up -d --build

dropin-frontend-mk-down: ## drop-in frontend e2e の mk overlay を撤去
	docker compose -f $(DROPIN_FRONTEND_COMPOSE) -f $(DROPIN_FRONTEND_MK_OVERLAY) down -v

dropin-frontend-swap-test: ## TS-A → mk-A 切替まで含む frontend e2e
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
##@ e2e: Cypress
e2e-submodule-init: ## cypress 用に submodule を初期化
	git submodule update --init --recursive third_party/misskey

# 本家フロントエンドを Docker 内でビルドする。数分〜10 分程度かかる。
# 成果物は third_party/misskey/packages/frontend/... 配下に出力される。
# パッチは submodule (shiroha-a/misskey-ts、tag 2026.5.4-mk.0) に直接コミット済み。
#
# CI=true を渡す理由: upstream 2026.5.2 で pnpm 10 → 11 に移行 (#17400 dep bump
# 系)、pnpm 11 は previous install (node_modules) を消す前に prompt を出す挙動が
# default。docker run は TTY 無し起動なので prompt が出せず ERR_PNPM_ABORTED_
# REMOVE_MODULES_DIR_NO_TTY で abort する。CI=true で skip させる。
e2e-frontend-build: ## フロントエンドをビルド (本番の bind-mount 先を上書きするので注意)
	docker run --rm -e CI=true -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/third_party/misskey \
		$(E2E_NODE_IMAGE) \
		bash -lc "corepack enable && corepack prepare pnpm@latest --activate && pnpm install --frozen-lockfile && pnpm build"

# Cypress ラッパーの依存を Docker 内で解決する。
e2e-deps: ## cypress の依存をインストール
	docker run --rm -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/e2e/cypress \
		$(E2E_NODE_IMAGE) \
		bash -lc "corepack enable && corepack prepare pnpm@latest --activate && pnpm install"

# ヘッドレスで cypress run を実行する。
# host network で動かして mk-go (localhost:3000) に直接届かせる。
e2e-run: ## Cypress E2E テストを実行
	docker run --rm --network=host -v $(PWD):$(E2E_WORKDIR) -w $(E2E_WORKDIR)/e2e/cypress \
		-e E2E_BASE_URL=$${E2E_BASE_URL:-http://localhost:3000} \
		$(E2E_CYPRESS_IMAGE) \
		cypress run --e2e --browser electron --config-file cypress.config.ts

# 開発者向けに cypress open を起動する (X11 forward が必要なので CI では使わない)。
e2e-open: ## Cypress を対話モードで開く
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

##@ 本番 UDS (実行注意)
uds-init: | $(UDS_COMPOSE) $(UDS_CONFIG) ## UDS 構成を初期化

# 本家 vite フロントエンドを docker 内でビルドする。初回は 3〜10 分程度かかる。
# 既存 e2e-frontend-build のエイリアス (成果物先が同じなので共有して OK)。
uds-frontend-build: e2e-frontend-build ## 本番向けフロントエンドをビルド (本番の配信物を差し替える)

uds-build: | $(UDS_COMPOSE) $(UDS_CONFIG) ## UDS スタックのイメージをビルド
	docker compose -f $(UDS_COMPOSE) build

uds-up: | $(UDS_COMPOSE) $(UDS_CONFIG) ## UDS スタックを起動
	docker compose -f $(UDS_COMPOSE) up -d --build

uds-down: | $(UDS_COMPOSE) ## UDS スタックを停止
	docker compose -f $(UDS_COMPOSE) down

# named volume も含めて完全削除する (DB データも全部消える)。
uds-down-v: | $(UDS_COMPOSE) ## UDS スタックを volume ごと削除
	docker compose -f $(UDS_COMPOSE) down -v

uds-logs: | $(UDS_COMPOSE) ## UDS スタックのログを表示
	docker compose -f $(UDS_COMPOSE) logs -f

uds-ps: | $(UDS_COMPOSE) ## UDS スタックのコンテナ一覧
	docker compose -f $(UDS_COMPOSE) ps

# Benchmark ― mk-go vs 本家 Misskey のストレステスト比較。
# k6 (Docker) で同一エンドポイントに負荷をかけ、レイテンシ・スループットを比較する。
# 結果は tests/bench/results/report.md に出力される。
BENCH_COMPOSE=tests/bench/docker-compose.bench.yml

##@ ベンチマーク
bench-up: ## k6 ベンチのスタックを起動
	docker compose -f $(BENCH_COMPOSE) up -d --build

bench-run: ## k6 ベンチを実行
	docker compose -f $(BENCH_COMPOSE) --profile bench up --abort-on-container-exit compare

bench-down: ## k6 ベンチのスタックを撤去
	docker compose -f $(BENCH_COMPOSE) down -v

bench-logs: ## k6 ベンチのログを表示
	docker compose -f $(BENCH_COMPOSE) logs -f

# Queue bench (#563): 3-way deliver/inbox throughput comparison across
# Misskey TS (BullMQ), mk-go (asynq), mk-go (mkq).
QUEUE_BENCH_COMPOSE=tests/queue-bench/docker-compose.queue-bench.yml

queue-bench-up: ## queue-bench スタックを起動
	docker compose -f $(QUEUE_BENCH_COMPOSE) up -d --build

queue-bench-seed: ## queue-bench 用のデータを投入
	# `--force-recreate` で seed container を毎回 fresh に作る (#1163)。
	#
	# `--no-deps` が要る。付けないと --force-recreate が依存 (app-asynq /
	# app-mkq) まで作り直し、それらの IP が変わる。nginx の upstream は
	# `server app-mkq:3000;` とホスト名で書かれていて **起動時に一度だけ**
	# 名前解決するため、nginx は死んだ IP を掴んだまま 502 を返し続ける。
	# seed の wait_health は例外にならない 502 を 240 秒受け取って
	# `not ready: None` で落ちる。IP が再利用されるかは運次第なので、
	# nightly が 8 回中 6 回落ちる flaky の正体だった (#2364)。
	# down → up を繰り返すと network が再作成されて新 ID になるが、profile
	# container は queue-bench-down (= `down -v`) の対象外で残る。古い container
	# は attach 先の network ID が変わったまま固定されて、次回 start 時に
	# `network <hash> not found` で失敗する非決定性を引き起こすため、毎回
	# 強制的に再作成する。同じ理由を queue-bench-outbound / -inbound / -report
	# にも適用している。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile bench up --abort-on-container-exit --force-recreate --no-deps seed
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

queue-bench-outbound: ## queue-bench の outbound 計測
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile outbound up --abort-on-container-exit --force-recreate --no-deps driver-outbound

queue-bench-inbound: ## queue-bench の inbound 計測
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile inbound up --abort-on-container-exit --force-recreate --no-deps driver-inbound

queue-bench-report: ## queue-bench のレポートを生成
	# queue-bench-seed と同じ理由で `--force-recreate` (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) --profile report up --abort-on-container-exit --force-recreate --no-deps report

queue-bench-all: queue-bench-seed queue-bench-outbound queue-bench-inbound queue-bench-report ## queue-bench を一通り実行

queue-bench-down: ## queue-bench スタックを撤去
	# `--remove-orphans` で profile container も含めて確実に cleanup する (#1163)。
	docker compose -f $(QUEUE_BENCH_COMPOSE) down -v --remove-orphans

queue-bench-logs: ## queue-bench のログを表示
	docker compose -f $(QUEUE_BENCH_COMPOSE) logs -f

# Auto-scale comparison bench (#1126 / #1120 tracker).
# 3 scenario (fixed16 / fixed64 / auto) を同一 mkq stack で逐次実行し、
# drain time / Redis client count を比較する。queue-bench との同居・
# 並列実行は想定しない (port は publish していないが volume / network 名は
# 別)。詳細: tests/queue-bench-autoscale/README.md (or docs/queue-bench.md)
AUTOSCALE_BENCH_DIR=tests/queue-bench-autoscale

queue-bench-autoscale-run: ## worker 数 fixed16 / fixed64 / auto を比較実行
	cd $(AUTOSCALE_BENCH_DIR) && ./run.sh

queue-bench-autoscale-down: ## autoscale ベンチのスタックを撤去
	cd $(AUTOSCALE_BENCH_DIR) && docker compose down -v --remove-orphans

queue-bench-autoscale-logs: ## autoscale ベンチのログを表示
	cd $(AUTOSCALE_BENCH_DIR) && docker compose logs -f

# Playwright e2e (#744 Phase 1)
#
# upstream Misskey TS 互換挙動を期待値に書いた spec を mk-go backend に
# 対して走らせ、drop-in 互換 regression を検出する。Phase 1 PR-1 では
# 基盤 + smoke 1 spec のみ。後続 PR で spec 拡充 + CI 統合する。
PLAYWRIGHT_COMPOSE=docker-compose.playwright.yml

##@ e2e: Playwright
playwright-up: ## Playwright スタック (mk-go backend) を起動
	docker compose -f $(PLAYWRIGHT_COMPOSE) up -d --build

playwright-test: ## Playwright spec を実行 (mk-go backend)
	# `--build` を付けて runner image を rebuild check させる。package.json
	# 更新時に node_modules が古いままにならないよう、毎回 build context を
	# 確認する (cache hit なら ms 単位で済むので overhead 無視可)。
	docker compose -f $(PLAYWRIGHT_COMPOSE) --profile test run --rm --build playwright-runner

playwright-down: ## Playwright スタックを撤去
	docker compose -f $(PLAYWRIGHT_COMPOSE) down -v

playwright-logs: ## Playwright スタックのログを表示
	docker compose -f $(PLAYWRIGHT_COMPOSE) logs -f

# Playwright TS validation (#744 Phase 1)
#
# 同 spec を upstream Misskey TS image (= 真の互換挙動の baseline) に対しても
# 走らせる。両方で pass = drop-in 互換が確認される、片方のみ pass = drift /
# spec 誤りとして調査対象。
PLAYWRIGHT_TS_OVERLAY=docker-compose.playwright.ts.yml

playwright-ts-up: ## Playwright スタック (Misskey TS backend) を起動
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) up -d --build

playwright-ts-test: ## Playwright spec を実行 (TS backend、upstream 追従時のみ)
	# `playwright-test` と同じく `--build` で runner image を最新化する。
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) --profile test run --rm --build playwright-runner

playwright-ts-down: ## Playwright TS スタックを撤去
	docker compose -f $(PLAYWRIGHT_COMPOSE) -f $(PLAYWRIGHT_TS_OVERLAY) down -v

# Differential e2e diff harness (#2089) ― mk-go と Misskey TS を同一版で
# 並列に立て、同一 endpoint のレスポンスを diff して entitycompat
# golden gate がカバーしない値レベル乖離を検出する。詳細は docs/diff-e2e.md。
# 隔離 stack (own network/volumes)、production UDS には触れない。
DIFF_COMPOSE=docker-compose.diff.yml

##@ e2e: 差分比較ハーネス
diff-up: ## 差分比較ハーネスのスタックを起動
	docker compose -f $(DIFF_COMPOSE) up -d --build

diff-test: ## mk-go ↔ TS の値レベル diff を実行
	docker compose -f $(DIFF_COMPOSE) --profile test run --rm --build diff-runner

diff-down: ## 差分比較ハーネスのスタックを撤去
	docker compose -f $(DIFF_COMPOSE) down -v

diff-logs: ## 差分比較ハーネスのログを表示
	docker compose -f $(DIFF_COMPOSE) logs -f

# Misskey 本家の backend e2e (test/e2e/**) を mk-go に向けて実行する。
# テスト本体には手を入れず、submodule 側の vitest 設定 2 ファイル
# (globalSetup / setupFiles) だけを差し替えている。上流でテストが増えれば
# 自動的にこちらの検証対象も増える。詳細は docs/upstream-backend-e2e.md。
#
# 『通らないことが正しい』テストは tests/upstream-e2e/known-divergences.json に
# 根拠付きで登録し、vitest の expected-failure として扱う。乖離が解消して通る
# ようになったテストは逆に落ちるので、一覧が陳腐化しない。
#
# ポートは本家 .github/misskey/test.yml に合わせてある (54312 / 56312 / 61812)。
UPSTREAM_E2E_COMPOSE=tests/upstream-e2e/compose.yml
UPSTREAM_E2E_CONFIG=tests/upstream-e2e/mkgo.yml
UPSTREAM_E2E_MISSKEY=third_party/misskey
UPSTREAM_E2E_BACKEND=$(UPSTREAM_E2E_MISSKEY)/packages/backend

##@ e2e: 本家 backend e2e
# submodule 側の依存を用意する。初回と submodule bump 後にだけ必要。
#
#  - misskey-js: exports が built/ を指すのでビルドしないと test/e2e が import できない。
#    frontend まで含む `pnpm build` (5-10 分) は e2e には不要なので呼ばない。
#  - .config/test.yml: 本家の utils.ts / setup が loadConfig() 経由で読む (port 等)。
#  - compile-config: loadConfig() は YAML ではなく built/.config.json を読むので、
#    NODE_ENV=test で .config/test.yml から生成しておく必要がある。
#  - build-pre: loadConfig() は built/meta.json も readFileSync する (無いと ENOENT)。
#    frontend の manifest は existsSync 判定なので無くてよい。
upstream-e2e-deps: ## 本家 backend e2e に必要な submodule 側の依存を用意 (初回のみ)
	cd $(UPSTREAM_E2E_MISSKEY) && \
		pnpm install --frozen-lockfile && \
		pnpm build-pre && \
		pnpm --filter misskey-js build && \
		cp .github/misskey/test.yml .config/ && \
		NODE_ENV=test pnpm --filter backend compile-config

upstream-e2e-up: ## 本家 backend e2e 用の PostgreSQL / Redis を起動
	docker compose -f $(UPSTREAM_E2E_COMPOSE) up -d --wait

upstream-e2e-migrate: ## e2e 用 DB にマイグレーションを適用
	go run ./cmd/migrate -config $(UPSTREAM_E2E_CONFIG) -direction up

# FILE で 1 ファイルだけ流せる: make upstream-e2e-test FILE=test/e2e/note.ts
upstream-e2e-test: build ## 本家 backend e2e を mk-go に対して実行
	cd $(UPSTREAM_E2E_BACKEND) && \
		MKGO_BIN=$(CURDIR)/built/misskey \
		MKGO_CONFIG=$(CURDIR)/$(UPSTREAM_E2E_CONFIG) \
		MKGO_CWD=$(CURDIR) \
		npx --no vitest run --config vitest.config.e2e.mkgo.ts $(FILE)

upstream-e2e: upstream-e2e-deps upstream-e2e-up upstream-e2e-migrate upstream-e2e-test ## 依存の用意からテストまで一括で実行

upstream-e2e-down: ## 本家 backend e2e 用のスタックを撤去 (volume ごと)
	docker compose -f $(UPSTREAM_E2E_COMPOSE) down -v

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
apicompat-routes: build ## route 一覧を JSON dump (stack 起動が必要)
	mkdir -p $(dir $(APICOMPAT_ROUTES))
	$(BUILD_DIR)/$(BINARY) -config $(APICOMPAT_CONFIG) -dump-routes -dump-routes-out $(APICOMPAT_ROUTES)

# 既存 APICOMPAT_ROUTES JSON だけ comparator にかけて matrix を再生成する
# (DB / Redis 接続不要)。matrix の format / category 表示を iterate する時に
# 毎回 build + dump し直さなくて済むよう用意した escape hatch。前提として
# `apicompat-routes` を最低一度走らせていること。
apicompat-render: ## route dump から互換性マトリクスを生成
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
apicompat: apicompat-routes apicompat-render ## API 互換性マトリクス docs/api-compat.md を生成

# --- entity shape drift (Layer 0 static API compatibility) ------------------
# mk-go の entity DTO struct を Misskey contract (misskey-js types.ts) と
# フィールド単位で突き合わせ、shape drift (欠落 / null性 / optional性) を検出する。
# サーバー / ブラウザ / Docker 不要で、CI では `go test ./...` 内の
# TestEntityShapeDrift gate として自動実行される。詳細は docs/shape-drift.md。

# golden snapshot (testdata/golden_schemas.json + golden_error_ids.json) を
# submodule から再生成する。third_party/misskey を upstream catch-up で bump
# したら必ず実行し、生成された snapshot を commit すること。
##@ 静的 parity ゲート (サーバー・Docker 不要)
shapecheck-gen: ## shape drift の golden snapshot を再生成
	go run ./tools/shapediff
	go run ./tools/erroriddiff
	go run ./tools/limitspec
	go run ./tools/permspec
	go run ./tools/securespec
	go run ./tools/schemadrift

# 全 family の drift を severity 付きで一覧表示する (gate にかける前の調査用)。
shapecheck-report: ## shape drift のレポートを出力
	go run ./tools/shapediff -report

# drift gate (L0 静的 + L2 実行時) をローカルで実行する (CI と同じ判定)。
# L0: TestEntityShapeDrift / L2: Test*ShapeL2 (Notification / Announcement / ...)。
shapecheck: ## レスポンス形状の drift を検査
	go test ./internal/entitycompat/... -run 'TestEntityShapeDrift|ShapeL2' -count=1 -v

# error-id / error-HTTP-status / error-kind drift gate をローカルで実行する。
# handler が emit する error id (inline / UUID 定数 / apierr helper / echo
# wrapper) と、Misskey が明示する HTTP status / kind discriminator を router
# 経由で endpoint に解決して突合する静的 gate。詳細は docs/shape-drift.md。
errorid-check: ## error id / HTTP status / kind の drift を検査
	go test ./internal/entitycompat/... -run 'TestErrorIDDrift|TestErrorHTTPStatusDrift|TestErrorKindDrift' -count=1 -v

# pagination limit-spec drift gate をローカルで実行する。handler が
# pagination.ClampLimit(limit, def, max) で渡す default/max literal を router
# 経由で endpoint に解決し、Misskey paramDef の default/maximum と突合する。
limitspec-check: ## ページネーションの default / max の drift を検査
	go test ./internal/entitycompat/... -run TestLimitSpecDrift -count=1 -v

# permission drift gate をローカルで実行する。mk-go の router middleware が
# Misskey の requireAdmin/requireModerator/requireCredential より緩くないか検証。
perm-check: ## router middleware の権限が upstream より緩くないか検査
	go test ./internal/entitycompat/... -run 'TestPermissionDrift|TestSecureDrift' -count=1 -v
