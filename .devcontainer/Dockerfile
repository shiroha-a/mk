FROM mcr.microsoft.com/devcontainers/go:1.26

# /go の所有権を vscode ユーザーに変更 (go mod download の権限エラー回避)
RUN chown -R vscode:vscode /go

# PostgreSQL client (pg_isready 等のユーティリティ)
RUN apt-get update && apt-get install -y postgresql-client && rm -rf /var/lib/apt/lists/*

# Node.js (フロントエンドビルド用)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && corepack enable \
    && corepack prepare pnpm@latest --activate

# golang-migrate CLI
RUN su vscode -c "go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"
