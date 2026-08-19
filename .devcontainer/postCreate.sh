#!/bin/bash
set -euo pipefail

echo "=== Go dependencies ==="
cd /workspace
go mod download

echo "=== Submodule init ==="
git submodule update --init --recursive third_party/misskey

echo "=== Wait for PostgreSQL ==="
for i in $(seq 1 30); do
    pg_isready -h localhost -p 5432 -U misskey && break
    echo "Waiting for PostgreSQL... ($i/30)"
    sleep 1
done

echo "=== Config file ==="
# cmd/migrate も mk-go 本体も -config (既定 .config/default.yml) を必ず読む。
# .config/* は gitignore なので clone 直後は存在せず、無いと failed to load config
# で落ちる。DB / Redis の向き先は compose の MK_DB_* / MK_REDIS_* が上書きする。
# url は example の https://example.tld/ のまま残る。開発中に絶対 URL を
# 永続化する経路 (drive file / emoji の publicUrl 等) を触るなら手で書き換える。
# **MK_URL で渡さないこと** — MK_* は viper で設定ファイルより優先されるので、
# internal/config のテストが fixture ではなくその値を読んで落ちる。
if [ ! -f .config/default.yml ]; then
    cp .config/default.yml.example .config/default.yml
    echo "created .config/default.yml"
fi

echo "=== Database migration ==="
make migrate-up || echo "Migration failed (may already be applied)"

echo "=== Frontend build ==="
cd /workspace/third_party/misskey
# Corepackがバージョン不一致時にダウンロード確認を求めないようにする
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
pnpm install --frozen-lockfile
pnpm build

echo "=== Done! Run 'make dev' to start the server ==="
