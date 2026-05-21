# syntax=docker/dockerfile:1.7
#
# `# syntax=` directive は BuildKit の RUN --mount=type=cache を有効に
# するために必須 (#432)。ローカル `docker build` でも CI (GitHub Actions
# runner) でも default frontend が 1.5+ になる現代では `1.7` で問題なし。

# Stage 1: Build Go binary
FROM golang:1.26-alpine AS builder

# Step 2 (#618) で chai2010/webp → gen2brain/webp (libwebp on wazero/WASM) に
# 切替えたので cgo 依存はゼロ。build-base (gcc + musl libc) は不要になった。
# git は go mod download 時の private module fetch に使うので残す。
RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
# go module cache を BuildKit cache mount に乗せると、再ビルド時の
# `go mod download` が依存に変更が無ければ no-op で済む (#432)。
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# third_party/misskey (submodule) がruntime stageのCOPY対象になるので、
# ビルド前に初期化されているか確認する。CIは docker.yml 側で submodules:
# recursive を指定して取得する。ローカル build 時はユーザに指示を出す。
RUN test -f third_party/misskey/packages/backend/assets/favicon.ico || \
    (echo "ERROR: third_party/misskey submodule not initialized (or partial clone)." && \
     echo "Run: git submodule update --init --recursive" && exit 1)

# twemojiは本家frontendがUnicode絵文字描画に使うSVG set。pnpm installで
# node_modulesに hoistされる前提 (make e2e-frontend-build等で install済み)。
# upstream 2026.5.2 #17381 で `@discordapp/twemoji/dist/svg` から
# `@misskey-dev/emoji-assets/built/twemoji` に asset path が移行。
RUN test -f third_party/misskey/packages/backend/node_modules/@misskey-dev/emoji-assets/built/twemoji/1f004.svg || \
    (echo "ERROR: twemoji assets not found (pnpm install not run?)." && \
     echo "Run: make e2e-frontend-build (installs third_party/misskey node_modules)" && exit 1)

# Go の build cache (`$GOCACHE` = /root/.cache/go-build) と module cache を
# BuildKit cache mount として永続化する。再ビルド時に変更の無いパッケージは
# 再コンパイルされずに layer 完成までが秒単位になる (#432)。
#
# CGO_ENABLED=0 + `-tags nodynamic` で static binary を生成する (#619)。
# - CGO_ENABLED=0: gen2brain/webp に切替えた今、cgo に依存するコードは無い。
# - -tags nodynamic: gen2brain/webp は default で purego (dlopen) 経由の
#   shared lib fallback を試みるため、これを切って WASM (wazero) 一本に
#   固定する。これがないと dlopen を呼ぶ層が残り完全 static にならない。
#
# Video thumbnail 抽出は build tag ではなく外部 service (Misskey TS 互換の
# videoThumbnailGenerator API) への HTTP/UDS 呼び出しで実現するので、ここに
# ffmpeg バイナリを同梱する必要は無い (#637 M2)。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /app/built/misskey ./cmd/misskey && \
    CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /app/built/migrate ./cmd/migrate

# Stage 2: Runtime
#
# distroless/static-debian13 (#621) を採用。Step 3 で binary が完全 static に
# なったので、shell / pkg manager / wget / coreutils 等を持たない最小 image
# でも起動できる。ca-certificates / tzdata は distroless に同梱されている
# ので apk add は不要。
#
# 注意: distroless は shell も wget も持たないので、healthcheck は
# `/app/misskey -healthcheck` で binary 自身に叩かせる (cmd/misskey/main.go
# の -healthcheck フラグ)。docker-compose.dropin*.mk.yml /
# docker-compose.federation.misskey.yml で使用。
FROM gcr.io/distroless/static-debian13

WORKDIR /app

COPY --from=builder /app/built/misskey /app/misskey
COPY --from=builder /app/built/migrate /app/migrate
COPY --from=builder /app/migration /app/migration

# 本家のpackages/backend/assets (favicon / icons等) をimageに焼き込む。
# bind-mountなしでも /favicon.ico / /static-assets/* 等が serve できる
# (issue #346)。third_party/misskey はsubmoduleなのでビルド前に
# `git submodule update --init --recursive` が必要。
COPY --from=builder /app/third_party/misskey/packages/backend/assets /app/static-assets
ENV MISSKEY_STATIC_DIR=/app/static-assets

# repo-level assets (ai.png等)。frontendが /assets/ai.png で参照する
# (mascotImageUrl のデフォルト)。submodule直下 (issue #360)。
COPY --from=builder /app/third_party/misskey/assets /app/repo-assets
ENV MISSKEY_REPO_ASSETS_DIR=/app/repo-assets

# twemoji SVG set (Unicode絵文字描画)。frontendが /twemoji/<codepoint>.svg
# で参照する。約18MB (issue #359)。upstream 2026.5.2 #17381 で
# `@misskey-dev/emoji-assets/built/twemoji` に asset path が移行した。
COPY --from=builder /app/third_party/misskey/packages/backend/node_modules/@misskey-dev/emoji-assets/built/twemoji /app/twemoji
ENV MISSKEY_TWEMOJI_DIR=/app/twemoji

# デフォルト設定ファイルをコピー (docker-compose でマウント上書き可能)。
# `.config/docker.yml` は gitignored で operator-local なので、image に
# 焼き込むのは `.example` 側 (placeholder 値を持つテンプレート)。
# 実際の運用では docker-compose の volume mount などで
# `/app/.config/default.yml` を上書きする想定。
COPY .config/docker.yml.example /app/.config/default.yml

EXPOSE 3000

# Misskey TS の Dockerfile が `useradd -u 991 -g 991 misskey` で UID/GID 991
# を採用しているので、drop-in 互換 (host volume `./files` の所有権が両者で
# 一致する) のため mk-go も同じ UID 991 で起動する (#621)。distroless static
# には UID 991 の /etc/passwd エントリは無いが、mk-go は os/user.Current()
# を呼ばないので numeric UID で問題なく動く。`:nonroot` tag (UID 65532) は
# drop-in 互換を壊すので使わない。
USER 991:991

ENTRYPOINT ["/app/misskey"]
CMD ["-config", ".config/default.yml"]
