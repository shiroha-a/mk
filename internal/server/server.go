package server

import (
	"bytes"
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
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/core/cache"
	"github.com/shiroha-a/mk/internal/core/chart"
	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/misc/redact"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
	queuemetrics "github.com/shiroha-a/mk/internal/queue/metrics"
	"github.com/shiroha-a/mk/internal/queue/runtimestats"
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

	// queueRuntimeStats は admin UI 向けの短期ランタイム統計 (worker 遅延 /
	// auto-scale 履歴)。/metrics は無認証公開で LB ACL 必須のため admin から
	// 読めない。その穴を埋めるプロセスローカルな snapshot (#2277)。
	queueRuntimeStats *runtimestats.Recorder
	// startedAt は admin/server-metrics が返す uptime の起点 (#2395)。
	startedAt time.Time
	// peerDeps はプラグイン同士の通信 (#2537) に要る一式。setupRoutes が
	// 署名・解決・ブロック判定を組み立ててから setupPlugins に渡す。
	// 未配線 (nil) のときは peer を宣言したプラグインも受け口を張らない。
	peerDeps *pluginPeerDeps
	// role は「このプロセスが Web を担うか、ジョブキューを担うか」(#2459)。
	// 既定 (RoleBoth) は env 未設定時の従来どおりの挙動。setupRoutes は
	// role を見て背景処理を出し分けるので、New より前に決まっている必要がある。
	role config.ProcessRole
	// queueOnlyServer は RoleQueue のときだけ立てる /healthz 用の最小 server。
	// Shutdown で閉じるために保持する。
	queueOnlyServer *http.Server

	// pluginRoles はプラグインのルートで権限を判定するための参照 (#2477)。
	// setupRoutes で roleService を作った後に入る。
	pluginRoles middleware.RoleChecker

	// pluginSetupErr はプラグイン登録の失敗を New まで運ぶ (#2478)。
	// setupRoutes は巨大で戻り値を持たないため、signature を変えずに
	// 伝播させる。**起動は必ず失敗させる**: 登録できなかったプラグインを
	// 黙って無効のまま動かすと、機能が消えた原因が分からない。
	pluginSetupErr error
	autoscale      *autoscaleRunner
	chartMgmt      *chart.ManagementService
	// mediaProxySecret は internal media proxy URL の HMAC 鍵。config に
	// 明示設定が無ければ DB (instance_secret) の生成値を使うため、New() で
	// 一度だけ解決して保持する。
	mediaProxySecret []byte

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

// Server timeouts. Go の http.Server はゼロ値が「無制限」なので、明示的に
// 設定しないと slow header / slow body / idle connection を送り続けるだけで
// file descriptor と goroutine を占有できてしまう。
//
// 値は upstream (Node) の既定に合わせてある。upstream も fastify 側で明示
// 設定はしておらず Node の既定に依拠しているので、そこに揃えるのが drop-in
// として自然で、かつ実運用で通っている値でもある。
//
//	headersTimeout 60s  -> ReadHeaderTimeout
//	requestTimeout 300s -> ReadTimeout
const (
	serverReadHeaderTimeout = 60 * time.Second
	serverReadTimeout       = 300 * time.Second
	// keep-alive の idle 上限。Node の既定 (keepAliveTimeout 5s) より長く
	// とってある。前段 nginx が upstream connection を keepalive で使い回す
	// 構成では 5s だと張り直しが頻発して割に合わない。
	serverIdleTimeout = 120 * time.Second
	// Go の既定 (DefaultMaxHeaderBytes) と同じ 1MiB。既定でも無制限では
	// ないが、明示しておかないと「header に上限があるか」をコードから
	// 読み取れない。
	serverMaxHeaderBytes = 1 << 20
)

// resolveMediaProxySecret returns the HMAC key used to sign internal media
// proxy URLs.
//
// 設定に `mediaProxySecret` があればそれを使う。無ければ DB (instance_secret)
// に永続化した乱数を使い、初回起動時に生成する。
//
// 以前は未設定時にインスタンス URL から導出していたが、URL は公開情報なので
// 誰でも同じ鍵を計算できた。mediaproxy の Authorize は署名を allowlist より
// **先に**見るため、署名を偽造できると allowlist ごと迂回でき、mk-go の
// media proxy が upstream と同じ「任意の公開 URL を取得する開いたプロキシ」に
// 退化していた。
//
// 起動ごとのメモリ生成では足りない。署名した URL を別プロセスが検証する構成が
// 成り立たなくなるし、role icon や announcement image のように allowlist に
// 載らない URL は再起動をまたいだ時点で 401 になる (allowlist にある
// avatar / drive / emoji / instance icon は fallback で救われるので、
// 壊れ方が経路ごとに変わって切り分けづらい)。
//
// DB から取れない場合は起動を止める。ここで弱い鍵に fallback すると、直した
// はずの迂回経路が黙って戻る。
func resolveMediaProxySecret(cfg *config.Config, db *gorm.DB) ([]byte, error) {
	if len(cfg.MediaProxySecret) > 0 {
		return cfg.MediaProxySecret, nil
	}
	secret, err := repository.NewInstanceSecretRepository(db).
		GetOrCreate(repository.MediaProxySecretKey)
	if err != nil {
		return nil, fmt.Errorf("resolve media proxy secret: %w", err)
	}
	slog.Info("media proxy secret: using the generated instance secret; set mediaProxySecret in config to manage it yourself")
	return secret, nil
}

// applyServerTimeouts sets read/idle timeouts and the header size cap on srv.
//
// WriteTimeout is deliberately left unset:
//   - streaming (WebSocket) と drive のファイル配信・media proxy は応答が
//     長時間に及ぶ。固定値を置くと正常な応答が途中で切れる
//   - WebSocket 側は gorilla が Upgrade 時に deadline を解除し、その後は
//     internal/stream が pong ベースで自前の deadline を張るので、server の
//     ReadTimeout は WebSocket を殺さない
func applyServerTimeouts(srv *http.Server) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = serverReadHeaderTimeout
	srv.ReadTimeout = serverReadTimeout
	srv.IdleTimeout = serverIdleTimeout
	srv.MaxHeaderBytes = serverMaxHeaderBytes
}

// New creates a new Server. Returns an error when the queue driver
// fails to initialise (e.g. mkq driver failing to PING Redis at
// startup).
func New(cfg *config.Config, db *gorm.DB, redis *cache.RedisClients) (*Server, error) {
	// 役割は環境変数で決まる (#2459)。矛盾した指定はここで落として、
	// 「起動はしたがジョブが処理されない」形の失敗にしない。
	role, err := config.ResolveProcessRole()
	if err != nil {
		return nil, err
	}
	if role != config.RoleBoth {
		slog.Info("process role selected", "role", string(role),
			"runsServer", role.RunsServer(), "runsQueue", role.RunsQueue())
	}
	logDevModeBanner(cfg)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	applyServerTimeouts(e.Server)
	// TLSServer は使っていない (TLS 終端は前段の reverse proxy) が、将来
	// e.StartTLS を使ったときに timeout 無しの server が生えるのを防ぐ。
	applyServerTimeouts(e.TLSServer)
	// 全 endpoint の c.JSON / c.Bind を共通 serializer に通す。実体は stdlib
	// encoding/json (fastJSONSerializer の doc 参照: goccy は #542 の panic で
	// revert、高速 encoder 化は #1142 で見送り)。
	e.JSONSerializer = fastJSONSerializer{}

	// trustProxyからIPExtractorを構成。詳細は buildIPExtractor のコメント。
	if nets := config.ParseTrustProxy(cfg.TrustProxy); len(nets) > 0 {
		e.IPExtractor = buildIPExtractor(nets)
	}

	// Global middleware
	e.Use(echomw.Recover())
	// Echo は Content-Type の charset を `UTF-8` (大文字) で出すが、本家
	// (Fastify) は `utf-8` (小文字) を返す。HTTP 的には case-insensitive でも、
	// 完全一致で分岐するクライアントが実在する (本家 e2e の simpleGet は
	// `text/html; charset=utf-8` に一致しないと body をパースしない) ため、
	// drop-in 互換として本家の表記に揃える。
	e.Use(lowercaseCharset)
	// Sentry middleware は Recover の直後に置く: panic を hub に送ったあと
	// Recover に巻き戻し、5xx の最終整形は echo に任せる。
	e.Use(mksentry.Middleware(cfg))
	e.Use(echomw.RequestID())
	e.Use(echomw.LoggerWithConfig(echomw.LoggerConfig{
		// `${uri}` は query を含むため、そのまま出すと `?i=<token>` の形で
		// 有効な credential がアクセスログに残る (redact package の doc 参照)。
		// `${custom}` に差し替えて秘密パラメータの値だけを伏せる。
		Format: "${time_rfc3339} ${method} ${custom} ${status} ${latency_human}\n",
		CustomTagFunc: func(c echo.Context, buf *bytes.Buffer) (int, error) {
			return buf.WriteString(redact.URI(c.Request().RequestURI))
		},
	}))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		// discovery endpoint は upstream が専用の hook で
		// `Access-Control-Allow-Headers: Accept` だけを広告する。ここで
		// グローバル CORS を通すと preflight に Origin/Content-Type/
		// Authorization まで載って乖離するので除外する (handler 側が
		// setDiscoveryCORS で必要なヘッダを全て付ける)。
		Skipper: func(c echo.Context) bool {
			return strings.HasPrefix(c.Request().URL.Path, "/.well-known/")
		},
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

	// body size 制限は auth.Authenticate より前に置く: auth は token 抽出のため
	// body を io.ReadAll するので、後に置くと巨大 body が auth で先に読まれて
	// bypass される (#1958 / #2075)。/api → 1MiB / inbox → 64KiB / multipart 除外。
	e.Use(middleware.BodyLimitByPath(cfg.MaxFileSize))
	// クリックジャッキング防止。upstream が ClientServerService の
	// onRequest hook で付けている X-Frame-Options: DENY に相当する。
	e.Use(middleware.FrameGuard())
	// 外部リンク遷移で閲覧中の URL が path ごと漏れるのを防ぐ (#2404)。
	// upstream には無い mk-go 独自の hardening。
	e.Use(middleware.ReferrerPolicy())

	// WWW-Authenticate は auth.Authenticate より外側に置く。auth は無効 token に
	// 対して自分で 401 を書くので、内側 (api グループ) に置くと middleware まで
	// 到達せずヘッダが付かない。/streaming の 401 も同じ経路なので、ここに
	// 置けば両方カバーできる。JSON の error body が無い応答では何もしない。
	e.Use(middleware.WWWAuthenticate())

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

		// admin UI 向けの短期ランタイム統計 (#2277)。config に関係なく常に
		// 有効。ring buffer 固定長なのでメモリは queue 数 × 数 KB。
		queueRuntimeStats: runtimestats.New(),

		// admin/server-metrics の uptime 起点 (#2395)。プロセス生成時刻なので
		// Server 生成時に確定させる。
		startedAt: time.Now(),

		// role は New の入口で解決済み (#2459)。setupRoutes がこれを見て
		// 背景処理を出し分ける。
		role: role,
	}

	// driver の dispatch hook を Prometheus と runtimestats の両方へ配る。
	// SetObserver は Start 前に呼ぶ必要がある (Start が closure に snapshot する)。
	if setter, ok := queueDriver.Server().(interface{ SetObserver(driver.Observer) }); ok {
		s.queueRuntimeStatsObserver(setter)
	}

	mediaProxySecret, err := resolveMediaProxySecret(cfg, db)
	if err != nil {
		// 弱い鍵に fallback すると、直したはずの allowlist 迂回が黙って戻る。
		return nil, err
	}
	s.mediaProxySecret = mediaProxySecret

	s.setupRoutes()
	if s.pluginSetupErr != nil {
		return nil, s.pluginSetupErr
	}

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
	runner, err := startAutoScale(context.Background(), s.config, s.queueDriver, s.queueMetrics, s.queueRuntimeStats)
	if err != nil {
		return fmt.Errorf("start autoscale: %w", err)
	}
	s.autoscale = runner
	s.registerSchedulerJobs()
	return nil
}

// registerSchedulerJobs registers every periodic cron job and starts the
// scheduler. Called from both the normal and autoscale start paths so the
// cron set lives in one place (#1563): adding a job here covers both paths.
func (s *Server) registerSchedulerJobs() {
	if s.queueScheduler == nil {
		return
	}
	// Register エラーは観測可能にする (Register 失敗は scheduler driver 不具合
	// の signal)。1 job の失敗で他の登録を止めない。
	jobs := []struct {
		name string
		fn   func() error
	}{
		{"chart", s.queueScheduler.RegisterChartJobs},
		{"instance refresh", s.queueScheduler.RegisterInstanceRefreshJob},
		{"retention", s.queueScheduler.RegisterRetentionJob},
		{"checkExpiredMutings", s.queueScheduler.RegisterCheckExpiredMutingsJob},
		{"clean", s.queueScheduler.RegisterCleanJob},
		{"chunkedUploadGc", s.queueScheduler.RegisterChunkedUploadGCJob},
		{"cleanRemoteNotes", s.queueScheduler.RegisterCleanRemoteNotesJob},
		{"orphanUserCleanup", s.queueScheduler.RegisterOrphanUserCleanupJob},
		{"checkModeratorsActivity", s.queueScheduler.RegisterCheckModeratorsActivityJob},
	}
	for _, j := range jobs {
		if err := j.fn(); err != nil {
			slog.Warn("scheduler register failed", "job", j.name, "err", err)
		}
	}
	if err := s.queueScheduler.Start(); err != nil {
		slog.Warn("scheduler start failed", "err", err)
	}
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
	// queue 側 (worker / autoscale / scheduler) は RoleServer では起動しない。
	// Web ノードもジョブを **enqueue** はするので queueClient は生かしたまま、
	// 消費側だけ止める (#2459)。
	if s.role.RunsQueue() {
		if err := s.queueServer.Start(); err != nil {
			return fmt.Errorf("start queue worker: %w", err)
		}
		// queueServer.Start 後にのみ controller を起動する (Start 前は driver.Resize
		// が ErrResizeNotSupported を返す = no pool yet)。startAutoScale は
		// jobQueueAutoScale=false なら nil runner を返す cheap no-op。
		runner, err := startAutoScale(context.Background(), s.config, s.queueDriver, s.queueMetrics, s.queueRuntimeStats)
		if err != nil {
			return fmt.Errorf("start autoscale: %w", err)
		}
		s.autoscale = runner
		s.registerSchedulerJobs()
	}
	// chart は **両方の role で回す**。メモリ上の集計バッファはプロセスごとに
	// 溜まり (API 経由の投稿は server、連合 inbox 由来は queue)、自分の分は
	// 自分で flush しないと失われる。
	if s.chartMgmt != nil {
		if err := s.chartMgmt.Start(context.Background()); err != nil {
			slog.Warn("chart management service start failed", "err", err)
		}
	}

	if !s.role.RunsServer() {
		return s.serveQueueOnly()
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
	// RoleServer では scheduler も worker も起動していない。mkq driver は
	// どちらの Shutdown も未起動で安全だが、asynq driver は inner にそのまま
	// 委譲するので、起動していない前提を持ち込まない (#2459)。
	if s.role.RunsQueue() {
		if s.queueScheduler != nil {
			s.queueScheduler.Shutdown()
		}
		s.queueServer.Shutdown()
	}
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
	// RoleQueue では echo を listen していないので、代わりに最小 server を閉じる。
	// echo.Shutdown 自体は listener 無しでも無害だが、閉じる対象を取り違えると
	// in-flight な /healthz が切られないまま抜ける。
	var err error
	if s.queueOnlyServer != nil {
		err = s.queueOnlyServer.Shutdown(ctx)
	} else {
		err = s.echo.Shutdown(ctx)
	}
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

// queueRuntimeStatsObserver wires the driver dispatch hook to both the
// Prometheus histograms and the admin-facing runtimestats recorder (#2277).
//
// 分離しているのは、Prometheus 側は長期の scrape 用 / runtimestats 側は
// admin UI が読む短期 snapshot 用で寿命が違うため。driver からは 1 本の
// Observer に見せる。
func (s *Server) queueRuntimeStatsObserver(setter interface{ SetObserver(driver.Observer) }) {
	obs := queuemetrics.MultiObserver{
		queuemetrics.NewObserver(s.queueMetrics),
		s.queueRuntimeStats,
	}
	setter.SetObserver(obs)
}

// lowercaseCharset rewrites `charset=UTF-8` to `charset=utf-8` in the
// Content-Type of every response.
//
// Echo は charset を大文字で出すが、本家 (Fastify) は小文字で返す。HTTP の
// 仕様上は case-insensitive なので実害は無いはずだが、Content-Type の完全一致
// で分岐するクライアントが実在する。本家の e2e ヘルパ (test/utils.ts の
// simpleGet) がまさにそれで、`text/html; charset=utf-8` に一致しないと HTML を
// パースせず body を null にしてしまう。drop-in 互換として表記を揃える。
func lowercaseCharset(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		res := c.Response()
		res.Before(func() {
			ct := res.Header().Get(echo.HeaderContentType)
			if fixed, ok := rewriteCharset(ct); ok {
				res.Header().Set(echo.HeaderContentType, fixed)
			}
		})
		return next(c)
	}
}

// rewriteCharset returns the Content-Type with an upper-case UTF-8 charset
// lowered, reporting whether a rewrite happened.
func rewriteCharset(ct string) (string, bool) {
	const upper = "charset=UTF-8"
	if idx := strings.Index(ct, upper); idx >= 0 {
		return ct[:idx] + "charset=utf-8" + ct[idx+len(upper):], true
	}
	// charset の無い application/json には付ける。本家 (Fastify) は JSON を
	// 常に `application/json; charset=utf-8` で返すので、Content-Type の完全
	// 一致で分岐するクライアントのために揃える。完全一致に限定して、他の
	// JSON 系 MIME (application/activity+json 等) には手を出さない。
	if ct == echo.MIMEApplicationJSON {
		// echo.MIMEApplicationJSONCharsetUTF8 は大文字 UTF-8 なので使わない。
		return echo.MIMEApplicationJSON + "; charset=utf-8", true
	}
	return ct, false
}
