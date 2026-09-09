#!/bin/bash
set -euo pipefail

echo "=== Go dependencies ==="
cd /workspace
go mod download

echo "=== Submodule init ==="
git submodule update --init --recursive third_party/misskey

# **Node / pnpm の版は submodule を唯一の定義にする (#2921)。** image には
# bootstrap 用の Node しか入っていない。CI (`pnpm/action-setup` + `.node-version`)
# と `make e2e-frontend-build` も同じ 2 ファイルを見るので、3 者が揃う。
#
# **拾えなかったら落とす。** 空のまま進むと不正なパッケージ名になるか、
# 黙って別の版で開発することになる。
echo "=== Node / pnpm (submodule の宣言に揃える) ==="
node_ver=$(tr -d '[:space:]' < third_party/misskey/.node-version)
pnpm_ver=$(sed -n 's/.*"packageManager"[[:space:]]*:[[:space:]]*"pnpm@\([^"]*\)".*/\1/p' \
    third_party/misskey/package.json)
if [ -z "$node_ver" ] || [ -z "$pnpm_ver" ]; then
    echo "submodule から Node / pnpm の版を読めない" >&2
    exit 1
fi
if [ "$(node -v)" != "v$node_ver" ]; then
    echo "node $(node -v) -> v$node_ver"
    curl -fsSL "https://deb.nodesource.com/setup_${node_ver%%.*}.x" | sudo -E bash -
    sudo apt-get install -y nodejs
fi
sudo npm i -g "pnpm@$pnpm_ver"
echo "node $(node -v) / pnpm $(pnpm --version)"

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
