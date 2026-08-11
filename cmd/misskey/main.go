package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/cache"
	"github.com/shiroha-a/mk/internal/model"
	mksentry "github.com/shiroha-a/mk/internal/sentry"
	"github.com/shiroha-a/mk/internal/server"
)

// healthcheckTimeout is the maximum time the -healthcheck mode waits for
// the local /healthz endpoint to respond.
const healthcheckTimeout = 3 * time.Second

func main() {
	configPath := flag.String("config", ".config/default.yml", "path to configuration file")
	healthcheckMode := flag.Bool("healthcheck", false, "perform a healthcheck (GET /healthz against the configured port) and exit 0/1")
	dumpRoutes := flag.Bool("dump-routes", false, "construct the server, dump registered HTTP routes as JSON, and exit (no listener started)")
	dumpRoutesOut := flag.String("dump-routes-out", "", "path to write -dump-routes JSON to; defaults to stdout. recommended to use a file so gorm/slog noise on stdout/stderr doesn't pollute the JSON consumer")
	doctorMode := flag.Bool("doctor", false, "run configuration / dependency / federation self-checks and exit 0 (ok) or 1 (failures found)")
	flag.Parse()

	if *healthcheckMode {
		os.Exit(runHealthcheck(*configPath))
	}

	// doctor はサーバーを起動しない。新規構築時に「まだ動かない状態」で
	// 詰まりを潰せることが要点なので、DB / Redis / 連合の検査だけを回す (#2463)。
	if *doctorMode {
		os.Exit(runDoctor(*configPath))
	}

	// ロガーの初期化
	// --dump-routes 時は stdout に JSON だけを出したいので、log は stderr に
	// 向ける (通常起動時は既存挙動に揃えて stdout)。
	logOut := io.Writer(os.Stdout)
	if *dumpRoutes {
		logOut = os.Stderr
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	slog.Info("starting Misskey (Go)", "version", config.MisskeyVersion, "mkGoVersion", config.MkGoVersion)

	// 設定ファイルの読み込み
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// --dump-routes は同じ config を共有する running instance と pid file /
	// Sentry init が衝突するので、これらを skip する。dump 後 exit するので
	// graceful shutdown 系の wiring も不要。
	if !*dumpRoutes {
		// pidFile が設定されていれば PID を書き込み、終了時に削除する。
		// 既存ファイルが生存中の他プロセスを指す場合は ErrAlreadyRunning で
		// 起動を拒否して二重起動を防ぐ (#497)。
		cleanupPid, err := server.WritePidFile(cfg.PidFile)
		if err != nil {
			slog.Error("failed to write pid file", "error", err)
			os.Exit(1)
		}
		defer cleanupPid()

		// Sentry init は他のサービスより前に走らせ、以降の起動エラーも捕捉対象にする。
		flushSentry, err := mksentry.Init(cfg)
		if err != nil {
			slog.Error("failed to init sentry", "error", err)
			os.Exit(1)
		}
		defer flushSentry()
	}

	// DB接続
	db, err := model.NewDatabase(cfg)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	// Redis接続
	redisClients, err := cache.NewRedisClients(cfg)
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	defer redisClients.Close()

	// HTTPサーバー起動
	srv, err := server.New(cfg, db, redisClients)
	if err != nil {
		slog.Error("failed to construct server", "error", err)
		os.Exit(1)
	}

	// --dump-routes: listener bind 前に echo の登録済 route 一覧を JSON で
	// stdout に流して exit する。tools/apicompat が Misskey TS api.json と
	// 突き合わせるための入力。DB/Redis 接続は handler 構築 (auth middleware の
	// repo wiring 等) で必須なので、ここまで実行してから dump する。
	if *dumpRoutes {
		out := io.Writer(os.Stdout)
		if *dumpRoutesOut != "" {
			f, err := os.Create(*dumpRoutesOut)
			if err != nil {
				slog.Error("failed to open dump-routes output", "path", *dumpRoutesOut, "error", err)
				os.Exit(1)
			}
			defer f.Close()
			out = f
		}
		if err := srv.DumpRoutes(out); err != nil {
			slog.Error("failed to dump routes", "error", err)
			os.Exit(1)
		}
		return
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Start(); err != nil {
			slog.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10_000_000_000) // 10 seconds
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	fmt.Println("Misskey stopped.")
}

// runHealthcheck performs a single HTTP GET against http://127.0.0.1:<port>/healthz
// using the port resolved from the same config the running server uses.
// distroless image には wget/curl が無いので、healthcheck をこの binary
// 自身で完結させるための専用モード (#621)。
func runHealthcheck(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: failed to load config: %v\n", err)
		return 1
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)
	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
