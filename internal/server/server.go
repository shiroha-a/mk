package server

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/cache"
	"github.com/shiroha-a/mk/internal/core/chart"
	corehashtag "github.com/shiroha-a/mk/internal/core/hashtag"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	"github.com/shiroha-a/mk/internal/repository"
	mksentry "github.com/shiroha-a/mk/internal/sentry"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/gorm"
)

// Server represents the HTTP server.
type Server struct {
	echo *echo.Echo
	// userRepo は CachedUserRepository でラップ済みの共有 instance。
	// auth middleware と setupRoutes の両方からこれを使うことで、片側の
	// mutation で他方の cache が stale 化するのを防ぐ (#300 3-3)。
	userRepo       repository.UserRepository
	config         *config.Config
	db             *gorm.DB
	redis          *cache.RedisClients
	auth           *middleware.AuthMiddleware
	queueDriver    driver.Driver
	queueClient    *queue.Client
	queueServer    *queue.Server
	queueScheduler *queue.Scheduler
	queueInspector *queue.Inspector
	chartMgmt      *chart.ManagementService
	// hashtagService は graceful shutdown で in-flight worker を drain する
	// ために参照する (#727)。fire-and-forget な OnNoteCreated worker (#719) が
	// SIGTERM 時に途中 kill されるのを避ける。
	hashtagService *corehashtag.Service

	// shutdownHooks はShutdown()時にqueue/HTTP echoより先に呼ばれる
	// ティッカー系ジョブの停止用。publisher goroutineをcleanに止める。
	shutdownHooks []func()
}

// registerShutdownHook registers fn to be invoked during Shutdown.
// Hooks run in registration order before the asynq / echo shutdown.
func (s *Server) registerShutdownHook(fn func()) {
	s.shutdownHooks = append(s.shutdownHooks, fn)
}

// buildIPExtractor returns an echo.IPExtractor that wraps Echo's standard
// XFF extractor with a UDS-safe fallback. Echo 標準の ExtractIPFromXFFHeader
// (echo/ip.go:252) は req.RemoteAddr が空文字 (UNIX domain socket 経由のとき
// net.SplitHostPort が "" を返す) のとき XFF 逆順走査で net.ParseIP("") が
// nil → directIP="" を早期 return してしまい、c.RealIP() が常に空文字を
// 返す。signin record / user_ip 記録 / rate-limit が破壊されるため (#703)、
// inner が空文字を返したケースのみ extractIPFallback で補う。
//
// 通常 (TCP listen) 経路では inner が valid IP を返すため fallback は走らず、
// 既存挙動と完全に互換。
func buildIPExtractor(trusted []*net.IPNet) echo.IPExtractor {
	opts := make([]echo.TrustOption, 0, len(trusted))
	for _, n := range trusted {
		opts = append(opts, echo.TrustIPRange(n))
	}
	inner := echo.ExtractIPFromXFFHeader(opts...)
	return func(req *http.Request) string {
		if ip := inner(req); ip != "" {
			return ip
		}
		return extractIPFallback(req, trusted)
	}
}

// extractIPFallback recovers a client IP for requests where Echo's standard
// extractor would return empty (typically UNIX domain socket connections
// with no RemoteAddr, behind nginx + UDS). XFF を逆順に走査し trusted ranges
// の外で最初に当たる解析可能な IP を返す。XFF が無ければ X-Real-IP に fallback。
// 結果が空文字となる場合 (header が一切無い) は "" を返し、上位の機能
// (signin record / user_ip / rate-limit) は空のまま記録する。
func extractIPFallback(req *http.Request, trusted []*net.IPNet) string {
	cleanCandidate := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		return s
	}
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		// untrusted side (client 側) を逆順走査で見つける
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := cleanCandidate(parts[i])
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if !isTrusted(ip) {
				return ip.String()
			}
		}
		// 全部 trusted ranges 内 (典型: client→nginx→mkgo が全部 private) なら
		// 一番左の解析可能な IP を返す。XFF[0] は upstream proxy が書く client IP。
		for _, p := range parts {
			candidate := cleanCandidate(p)
			if net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}
	if r := cleanCandidate(req.Header.Get("X-Real-IP")); r != "" {
		return r
	}
	return ""
}

// gzipConfig returns the GzipConfig used by the global middleware stack.
// Shared with gzip_test.go so production and tests stay in sync.
func gzipConfig() echomw.GzipConfig {
	// MinLength=1024 で小さい body の gzip overhead を回避し、/streaming は
	// WebSocket frame を壊さないよう Skipper で除外する (#413 Phase 3 #12)。
	return echomw.GzipConfig{
		Skipper: func(c echo.Context) bool {
			// WebSocket upgrade 経路 (/streaming) は frame 単位の bidirectional
			// 通信で gzip すると壊れる。
			return c.Path() == "/streaming"
		},
		Level:     gzip.DefaultCompression,
		MinLength: 1024,
	}
}

// New creates a new Server. Returns an error when the queue driver
// fails to initialise (e.g. mkq driver failing to PING Redis at
// startup).
func New(cfg *config.Config, db *gorm.DB, redis *cache.RedisClients) (*Server, error) {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// echo の c.JSON / c.Bind 経路を goccy/go-json に差し替える (#507)。
	// reflection cost が下がって全 endpoint が広く高速化する。
	e.JSONSerializer = fastJSONSerializer{}

	// trustProxyからIPExtractorを構成。詳細は buildIPExtractor のコメント。
	if nets := config.ParseTrustProxy(cfg.TrustProxy); len(nets) > 0 {
		e.IPExtractor = buildIPExtractor(nets)
	}

	// Global middleware
	e.Use(echomw.Recover())
	// Sentry middleware は Recover の直後に置く: panic を hub に送ったあと
	// Recover に巻き戻し、5xx の最終整形は echo に任せる。
	e.Use(mksentry.Middleware(cfg))
	e.Use(echomw.RequestID())
	e.Use(echomw.LoggerWithConfig(echomw.LoggerConfig{
		Format: "${time_rfc3339} ${method} ${uri} ${status} ${latency_human}\n",
	}))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization},
	}))
	// gzip response compression (#413 Phase 3 #12)。Misskey TS は nginx
	// 前段で gzip するのが定石だが、mk-go は単体運用も想定するので app 側
	// で提供する。設定は gzipConfig() に集約してテストと共有。
	e.Use(echomw.GzipWithConfig(gzipConfig()))

	// CachedUserRepository を一度だけ構築して auth middleware と
	// setupRoutes (services) が同じ cache を共有するようにする (#300 3-3)。
	userRepo := repository.NewCachedUserRepository(repository.NewUserRepository(db))
	accessTokenRepo := repository.NewAccessTokenRepository(db)

	auth := middleware.NewAuthMiddleware(userRepo, accessTokenRepo)
	e.Use(auth.Authenticate())

	// queue driver セットアップ: jobQueueDriver config で asynq / mkq を
	// 選択。Host が UNIX domain socket パス ("/" 始まり) のときは driver
	// 内部で Network を unix に切り替える。
	queueDriver, err := buildQueueDriver(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("server: build queue driver: %w", err)
	}
	queueClient := queue.NewClient(queueDriver)
	applyClientPolicies(queueClient, cfg)
	queueServer := queue.NewServer(queueDriver)
	queueScheduler := queue.NewScheduler(queueDriver)
	queueInspector := queue.NewInspector(queueDriver)

	s := &Server{
		echo:           e,
		userRepo:       userRepo,
		config:         cfg,
		db:             db,
		redis:          redis,
		auth:           auth,
		queueDriver:    queueDriver,
		queueClient:    queueClient,
		queueServer:    queueServer,
		queueScheduler: queueScheduler,
		queueInspector: queueInspector,
	}

	s.setupRoutes()

	return s, nil
}

// Handler returns the underlying http.Handler for use with httptest.
// E2Eテスト等でサーバーを外部から起動する場合に使う���
func (s *Server) Handler() http.Handler {
	return s.echo
}

// Start begins listening on the configured port (or UNIX domain socket) and
// launches the asynq worker.
//
// If s.config.Socket is non-empty the HTTP server binds to that path instead
// of a TCP port. This matches Misskey 本家 YAML の `socket` / `chmodSocket`
// 設定と同じ運用感覚で使える。
func (s *Server) Start() error {
	if err := s.queueServer.Start(); err != nil {
		return fmt.Errorf("start queue worker: %w", err)
	}
	if s.queueScheduler != nil {
		if err := s.queueScheduler.RegisterChartJobs(); err != nil {
			slog.Warn("chart scheduler register failed", "err", err)
		}
		if err := s.queueScheduler.RegisterInstanceRefreshJob(); err != nil {
			slog.Warn("instance refresh scheduler register failed", "err", err)
		}
		if err := s.queueScheduler.RegisterRetentionJob(); err != nil {
			slog.Warn("retention scheduler register failed", "err", err)
		}
		if err := s.queueScheduler.Start(); err != nil {
			slog.Warn("scheduler start failed", "err", err)
		}
	}
	if s.chartMgmt != nil {
		if err := s.chartMgmt.Start(context.Background()); err != nil {
			slog.Warn("chart management service start failed", "err", err)
		}
	}

	if s.config.Socket != "" {
		ln, err := config.ListenUnixSocket(s.config.Socket, s.config.ChmodSocket)
		if err != nil {
			return err
		}
		s.echo.Listener = ln
		slog.Info("starting Misskey server",
			"socket", s.config.Socket, "url", s.config.URL)
		// Echo.Start は内部で net.Listen してしまうので、ここでは Start では
		// なく Serve を使って既に張った listener を使う。
		if err := s.echo.Server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	addr := fmt.Sprintf(":%d", s.config.Port)
	slog.Info("starting Misskey server", "addr", addr, "url", s.config.URL)
	return s.echo.Start(addr)
}

// Shutdown gracefully shuts down the server, the asynq worker and
// any background services such as the chart management loop.
func (s *Server) Shutdown(ctx context.Context) error {
	// 登録順にshutdown hookを走らせる。publisher goroutineをclean停止。
	for _, hook := range s.shutdownHooks {
		hook()
	}
	// hashtag service の in-flight worker (#719 fire-and-forget) を ctx 期限
	// 内で drain する (#727)。typical case では即返り、長時間動く worker は
	// ctx timeout で諦める (idempotent な RecordMention なので次回再カウント)。
	if s.hashtagService != nil {
		if err := s.hashtagService.Shutdown(ctx); err != nil {
			slog.Warn("hashtag service shutdown timed out", "err", err)
		}
	}
	if s.chartMgmt != nil {
		s.chartMgmt.Stop(ctx)
	}
	if s.queueScheduler != nil {
		s.queueScheduler.Shutdown()
	}
	s.queueServer.Shutdown()
	// queueClient.Close を直接呼ばないこと。queueDriver.Close が
	// Client / Inspector を含むサブコンポーネントの Close を一括処理
	// するため、ここで呼ぶと asynq driver では同じ *asynq.Client を
	// 二重 close して pool.ErrPoolClosed の warn log が毎回出る。
	// mkq driver は Client.Close が no-op で driver 本体に集約する
	// 仕様なので、driver.Close 一本に統一する方が両 driver で対称。
	if s.queueDriver != nil {
		if err := s.queueDriver.Close(); err != nil {
			slog.Warn("queue driver close failed", "err", err)
		}
	}
	err := s.echo.Shutdown(ctx)
	// UDS listen していた場合、Shutdown で net.Listener.Close() は呼ばれる
	// が、ソケットファイル自体は残るので明示的に unlink しておく。
	if rmErr := config.RemoveUnixSocket(s.config.Socket); rmErr != nil {
		slog.Warn("failed to remove socket file", "socket", s.config.Socket, "err", rmErr)
	}
	return err
}

// setChartManagement registers the chart management service so its
// save loop is started/stopped together with the HTTP server. Called
// from setupRoutes after the chart engines are constructed.
func (s *Server) setChartManagement(m *chart.ManagementService) {
	s.chartMgmt = m
}
