package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/cache"
	"github.com/shiroha-a/mk/internal/core/chart"
	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
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
	// queueMetrics は /metrics endpoint と auto-scale controller の双方が
	// 共有する Prometheus metric bundle (#1122 で declare、#1125 で
	// controller が ScaleEventsTotal を increment)。EnableMetrics=false でも
	// auto-scale が活きていれば counter は内部 increment するが、register
	// されないので外部公開はされない。
	queueMetrics *queuemetrics.Metrics
	autoscale    *autoscaleRunner
	chartMgmt    *chart.ManagementService
	// deliverSvc は federation deliver service への参照。本番では asynq
	// 経由で deliver を enqueue するが、test (#780) で queue を bypass する
	// ための SetSyncDeliverHookForTest を呼べるよう参照を保持する。
	deliverSvc *corefederation.DeliverService

	// shutdownHooks は Shutdown() 時に queue / HTTP echo より先に呼ばれる。
	// ctx-aware にすることで graceful drain (例: hashtag service の
	// in-flight worker、#727) を hook で wire できる。シンプルな
	// stop/close 系 hook は引数 _ で受け流すだけ。
	shutdownHooks []func(context.Context)
}

// registerShutdownHook registers fn to be invoked during Shutdown.
// Hooks run in registration order before the asynq / echo shutdown.
// ctx は Shutdown() の caller から伝播され、graceful drain の deadline
// として使える (#764)。
func (s *Server) registerShutdownHook(fn func(context.Context)) {
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
	// scheduled note 機能の driver capability (= mkq のみ確実に動作、asynq
	// は task ID 仕様の制約で clearSchedule が困難なため無効化)。空文字列
	// は mkq に正規化される config 経路だが defensive に判定する
	// (#1045 Phase 2-C)。
	queueClient.SetSupportsScheduledNote(cfg.JobQueueDriver == "" || cfg.JobQueueDriver == "mkq")
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
		queueMetrics:   newQueueMetrics(queueDriver),
	}

	s.setupRoutes()

	return s, nil
}

// Handler returns the underlying http.Handler for use with httptest.
// E2Eテスト等でサーバーを外部から起動する場合に使う���
func (s *Server) Handler() http.Handler {
	return s.echo
}

// DumpedRoute represents a single registered Echo route, used by DumpRoutes
// to expose mk-go's HTTP surface for tooling (api-compat matrix etc).
type DumpedRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// DumpedRoutes is the JSON payload emitted by DumpRoutes.
type DumpedRoutes struct {
	MisskeyVersion string        `json:"misskeyVersion"`
	MkGoVersion    string        `json:"mkGoVersion"`
	Routes         []DumpedRoute `json:"routes"`
}

// DumpRoutes writes all registered Echo routes as JSON to w.
// `cmd/misskey -dump-routes` から呼ばれ、tools/apicompat が Misskey TS の
// endpoint 集合 (filename-derived + ApiServerService 直登録) と突き合わせる
// ための入力になる。echo 内部の "/*" catch-all 等は除外しない (caller 側で
// 正規化する想定)。出力 path は string sort で安定化。
//
// Phase 2 で TS の api.json ベースに切り替える際もこの method の出力形式は
// そのまま使える想定 (input source 側だけ差し替え)。
func (s *Server) DumpRoutes(w io.Writer) error {
	routes := s.echo.Routes()
	dumped := make([]DumpedRoute, 0, len(routes))
	for _, r := range routes {
		dumped = append(dumped, DumpedRoute{Method: r.Method, Path: r.Path})
	}
	sort.Slice(dumped, func(i, j int) bool {
		if dumped[i].Path != dumped[j].Path {
			return dumped[i].Path < dumped[j].Path
		}
		return dumped[i].Method < dumped[j].Method
	})
	payload := DumpedRoutes{
		MisskeyVersion: config.MisskeyVersion,
		MkGoVersion:    config.MkGoVersion,
		Routes:         dumped,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// StartBackgroundForTest starts the asynq queue worker (and optional
// scheduler / chart management) without launching the HTTP listener.
// 用途: e2e_federation 系のテストで `httptest.Server` 経由で echo handler を
// 外部 listener にぶら下げつつ、deliver / inbox 処理など async queue 経路も
// 動かしたい場合 (#435)。本番経路 (Server.Start) は HTTP listener を含めて
// すべて起動するが、テストでは echo を test 側が制御するためここから queue
// 部分だけ抽出する。
//
// production code から呼ばないこと。
func (s *Server) StartBackgroundForTest() error {
	if err := s.queueServer.Start(); err != nil {
		return fmt.Errorf("start queue worker: %w", err)
	}
	// queueServer.Start 後にのみ controller を起動する (Start 前は driver.Resize
	// が ErrResizeNotSupported を返す = no pool yet)。startAutoScale は
	// jobQueueAutoScale=false なら nil runner を返す cheap no-op。
	runner, err := startAutoScale(context.Background(), s.config, s.queueDriver, s.queueMetrics)
	if err != nil {
		return fmt.Errorf("start autoscale: %w", err)
	}
	s.autoscale = runner
	if s.queueScheduler != nil {
		// Server.Start と同じく Register エラーは観測可能にする (test 出力の
		// noise にはならない、Register 失敗は scheduler driver 不具合の signal)。
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
	return nil
}

// SetSyncDeliverHookForTest replaces the asynq deliver enqueue with the
// supplied synchronous hook. e2e_federation 系テストで queue worker 経由の
// deliver が動かない/動かしたくないシナリオで、sign + HTTP POST を inline
// で実行する用途。fn=nil で本番経路 (queue) に戻る。
//
// production code から呼ばないこと (#780)。
func (s *Server) SetSyncDeliverHookForTest(fn func(payload queue.DeliverPayload) error) {
	if s.deliverSvc != nil {
		s.deliverSvc.SetSyncDeliverHookForTest(fn)
	}
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
	// queueServer.Start 後にのみ controller を起動する (Start 前は driver.Resize
	// が ErrResizeNotSupported を返す = no pool yet)。startAutoScale は
	// jobQueueAutoScale=false なら nil runner を返す cheap no-op。
	runner, err := startAutoScale(context.Background(), s.config, s.queueDriver, s.queueMetrics)
	if err != nil {
		return fmt.Errorf("start autoscale: %w", err)
	}
	s.autoscale = runner
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
	// 登録順に shutdown hook を走らせる。publisher goroutine の clean 停止と、
	// graceful drain が必要な service (hashtag #727 等) の Shutdown(ctx) を
	// 兼ねる。hook が ctx を取るので timeout 共有が可能 (#764)。
	for _, hook := range s.shutdownHooks {
		hook(ctx)
	}
	if s.chartMgmt != nil {
		s.chartMgmt.Stop(ctx)
	}
	// queueServer.Shutdown 前に controller を停止 (Resize 呼びの停止 = pool に
	// 余計な変更が走らない状態で queueServer の pool を畳める)。Shutdown ctx を
	// 伝播することで autoscale の goroutine drain にも graceful deadline が効く。
	s.autoscale.Stop(ctx)
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
