package config

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// MkGoVersion is the mk-go version. Override at build time via:
//
//	go build -ldflags "-X github.com/shiroha-a/mk/internal/config.MkGoVersion=1.0.0"
var MkGoVersion = "0.9.1"

// MisskeyVersion is the compatible Misskey version. Override at build time via:
//
//	go build -ldflags "-X github.com/shiroha-a/mk/internal/config.MisskeyVersion=2026.5.4"
var MisskeyVersion = "2026.5.4"

// RedisOptions represents Redis connection configuration.
type RedisOptions struct {
	Host string `mapstructure:"host"`
	// Path is an ioredis-style alias for connecting via a UNIX domain socket.
	// Misskey TS は内部で ioredis を使っており、`example.yml` の "You can
	// specify more ioredis options..." 経由で `redis.path: /run/valkey.sock`
	// を書いている operator が一定数いる。同じ config を mk と共有する drop-in
	// 切替で詰まらないよう、Path が指定されていれば Host より優先して unix
	// socket 接続にする (#519)。
	Path     string `mapstructure:"path"`
	Port     int    `mapstructure:"port"`
	Family   int    `mapstructure:"family"`
	Pass     string `mapstructure:"pass"`
	DB       int    `mapstructure:"db"`
	Prefix   string `mapstructure:"prefix"`
	Username string `mapstructure:"username"`
	// Pool tuning (Go固有。Misskey TS設定にはない)
	PoolSize *int `mapstructure:"poolSize"`
}

// DBOptions represents PostgreSQL connection configuration.
type DBOptions struct {
	Host         string            `mapstructure:"host"`
	Port         int               `mapstructure:"port"`
	DB           string            `mapstructure:"db"`
	User         string            `mapstructure:"user"`
	Pass         string            `mapstructure:"pass"`
	DisableCache bool              `mapstructure:"disableCache"`
	Extra        map[string]string `mapstructure:"extra"`
	// Pool tuning (Go固有。Misskey TS設定にはない)
	MaxOpenConns    *int `mapstructure:"maxOpenConns"`
	MaxIdleConns    *int `mapstructure:"maxIdleConns"`
	ConnMaxLifetime *int `mapstructure:"connMaxLifetime"` // 秒
	ConnMaxIdleTime *int `mapstructure:"connMaxIdleTime"` // 秒
}

// DBSlaveOptions represents a single read-replica PostgreSQL connection.
// Mirrors Misskey's `dbSlaves: [{host, port, db, user, pass}]` YAML entries.
// SSL/extra options are inherited from the primary DBOptions at DSN build time.
type DBSlaveOptions struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	DB   string `mapstructure:"db"`
	User string `mapstructure:"user"`
	Pass string `mapstructure:"pass"`
}

// MeilisearchOptions represents Meilisearch configuration.
type MeilisearchOptions struct {
	Host   string `mapstructure:"host"`
	Port   string `mapstructure:"port"`
	APIKey string `mapstructure:"apiKey"`
	SSL    bool   `mapstructure:"ssl"`
	Index  string `mapstructure:"index"`
	Scope  string `mapstructure:"scope"`
}

// FulltextSearchOptions represents fulltext search configuration.
type FulltextSearchOptions struct {
	Provider string `mapstructure:"provider"`
}

// SentryOptions mirrors the subset of `Sentry.NodeOptions` from the upstream
// Misskey YAML schema that maps cleanly onto Go's sentry-go SDK. Fields not
// covered (e.g. integration callbacks) are ignored on the Go backend.
type SentryOptions struct {
	DSN              string  `mapstructure:"dsn"`
	Environment      string  `mapstructure:"environment"`
	Release          string  `mapstructure:"release"`
	SampleRate       float64 `mapstructure:"sampleRate"`
	TracesSampleRate float64 `mapstructure:"tracesSampleRate"`
	Debug            bool    `mapstructure:"debug"`
	ServerName       string  `mapstructure:"serverName"`
}

// SentryBackendOptions mirrors `sentryForBackend` from Misskey config YAML.
// EnableNodeProfiling refers to a Node-only Sentry integration; on the Go
// backend it is parsed for compatibility but reported as a no-op at boot.
type SentryBackendOptions struct {
	EnableNodeProfiling bool          `mapstructure:"enableNodeProfiling"`
	Options             SentryOptions `mapstructure:"options"`
}

// LoggingOptions represents logging configuration.
type LoggingOptions struct {
	SQL *SQLLoggingOptions `mapstructure:"sql"`
}

// SQLLoggingOptions represents SQL logging configuration.
type SQLLoggingOptions struct {
	DisableQueryTruncation bool `mapstructure:"disableQueryTruncation"`
	EnableQueryParamLog    bool `mapstructure:"enableQueryParamLogging"`
}

// Source represents the raw YAML configuration file structure.
type Source struct {
	URL               string `mapstructure:"url"`
	Port              int    `mapstructure:"port"`
	Socket            string `mapstructure:"socket"`
	ChmodSocket       string `mapstructure:"chmodSocket"`
	DisableHSTS       bool   `mapstructure:"disableHsts"`
	EnableIPRateLimit *bool  `mapstructure:"enableIpRateLimit"`
	// DisableEndpointRateLimits, when true, drops the per-endpoint rate
	// limit table entirely. Intended for benchmarking parity with Misskey TS
	// `NODE_ENV=development`. Never enable in production. Default false.
	DisableEndpointRateLimits *bool                  `mapstructure:"disableEndpointRateLimits"`
	DB                        DBOptions              `mapstructure:"db"`
	DBReplications            bool                   `mapstructure:"dbReplications"`
	DBSlaves                  []DBSlaveOptions       `mapstructure:"dbSlaves"`
	Redis                     RedisOptions           `mapstructure:"redis"`
	RedisForPubsub            *RedisOptions          `mapstructure:"redisForPubsub"`
	RedisForJobQueue          *RedisOptions          `mapstructure:"redisForJobQueue"`
	RedisForTimelines         *RedisOptions          `mapstructure:"redisForTimelines"`
	RedisForReactions         *RedisOptions          `mapstructure:"redisForReactions"`
	FulltextSearch            *FulltextSearchOptions `mapstructure:"fulltextSearch"`
	Meilisearch               *MeilisearchOptions    `mapstructure:"meilisearch"`
	SetupPassword             string                 `mapstructure:"setupPassword"`
	Proxy                     string                 `mapstructure:"proxy"`
	ProxySmtp                 string                 `mapstructure:"proxySmtp"`
	ProxyBypassHosts          []string               `mapstructure:"proxyBypassHosts"`
	AllowedPrivateNetworks    []string               `mapstructure:"allowedPrivateNetworks"`
	MaxFileSize               *int64                 `mapstructure:"maxFileSize"`
	ClusterLimit              *int                   `mapstructure:"clusterLimit"`
	ID                        string                 `mapstructure:"id"`
	OutgoingAddress           string                 `mapstructure:"outgoingAddress"`
	OutgoingAddressFamily     string                 `mapstructure:"outgoingAddressFamily"`

	DeliverJobConcurrency      *int `mapstructure:"deliverJobConcurrency"`
	InboxJobConcurrency        *int `mapstructure:"inboxJobConcurrency"`
	RelationshipJobConcurrency *int `mapstructure:"relationshipJobConcurrency"`
	DeliverJobPerSec           *int `mapstructure:"deliverJobPerSec"`
	InboxJobPerSec             *int `mapstructure:"inboxJobPerSec"`
	RelationshipJobPerSec      *int `mapstructure:"relationshipJobPerSec"`
	DeliverJobMaxAttempts      *int `mapstructure:"deliverJobMaxAttempts"`
	InboxJobMaxAttempts        *int `mapstructure:"inboxJobMaxAttempts"`
	// `<queue>JobKeepFailed` は failed bucket retention の最大件数。
	// 0 = retention 無し (= 蓄積し続ける) なので、operator が明示的に
	// 「全部残す」運用にしたい場合のみ 0 を設定する。未指定なら mk-go
	// の internal default (defaultKeepFailed) が適用される。
	DeliverJobKeepFailed *int `mapstructure:"deliverJobKeepFailed"`
	InboxJobKeepFailed   *int `mapstructure:"inboxJobKeepFailed"`

	// `<queue>JobKeepCompleted` は completed bucket retention の最大
	// 件数。意味的には KeepFailed と同じ「件数で上限を切る」。0 で
	// 蓄積無制限 (= UDS 観測で実害があった BullMQ default 挙動) に
	// opt-out 可能。未指定なら mk-go internal default (TS と同じ 30)
	// が適用される (#1193)。
	DeliverJobKeepCompleted *int `mapstructure:"deliverJobKeepCompleted"`
	InboxJobKeepCompleted   *int `mapstructure:"inboxJobKeepCompleted"`

	// JobQueueAutoScale toggles the AIMD auto-scale controller (#1120).
	// Default false. When true, queues without an explicit
	// `<queue>JobConcurrency` are managed by the controller within
	// [MinWorkers, MaxWorkers]. See docs/design/auto-scale-job-workers.md.
	JobQueueAutoScale bool `mapstructure:"jobQueueAutoScale"`

	// MaxWorkers is the per-queue worker upper bound used by the
	// auto-scale controller. Default: runtime.NumCPU() * 16.
	MaxWorkers *int `mapstructure:"maxWorkers"`

	// MinWorkers is the per-queue worker lower bound. Default: 4.
	MinWorkers *int `mapstructure:"minWorkers"`

	// MaxWorkersGlobal, when set, caps the sum of workers across all
	// auto-scaled queues for this process. Default: unset (no cap; each
	// queue scales up to MaxWorkers independently). Use only in
	// multi-pod deployments where per-process budget matters.
	MaxWorkersGlobal *int `mapstructure:"maxWorkersGlobal"`

	// AutoScaleCooldownSeconds is the minimum interval between scale
	// events per queue. Default: 1 second (per ADR §3.4).
	AutoScaleCooldownSeconds *int `mapstructure:"autoScaleCooldownSeconds"`

	// JobQueueDriver selects the worker / inspector implementation
	// behind internal/queue. "asynq" (default) uses
	// hibiken/asynq; "mkq" uses the BullMQ-compatible
	// shiroha-a/mkq library. Empty / unset = "asynq".
	JobQueueDriver string `mapstructure:"jobQueueDriver"`

	MediaProxy              string `mapstructure:"mediaProxy"`
	MediaProxySecret        string `mapstructure:"mediaProxySecret"`
	VideoThumbnailGenerator string `mapstructure:"videoThumbnailGenerator"`
	// VideoThumbnailGeneratorMode picks the wire used to ask the generator:
	//   "post" (default, mk-go) — POST <gen>/thumbnail with multipart/form-data
	//                             (`file` field). nekonoverse/video-thumb と同
	//                             仕様。SSRF surface を generator 側に持ち
	//                             込まない。
	//   "get"                   — GET <gen>/thumbnail.webp?thumbnail=1&url=<URL>
	//                             Misskey TS の videoThumbnailGenerator 仕様
	//                             に互換 (#637 review)。
	VideoThumbnailGeneratorMode string `mapstructure:"videoThumbnailGeneratorMode"`
	// NSFWDetectorURL は外部 NSFW 判定 service の endpoint。空なら自動判定
	// disabled で手動 isSensitive フラグのみ機能する (#406)。mk-go は
	// backend に ML runtime を抱えない方針なので、operator が任意の互換
	// service を立てて URL を指定する。Wire: POST body=image/video bytes,
	// Content-Type=<MIME>, response JSON `{"score": float64}` (0..1)。
	NSFWDetectorURL string `mapstructure:"nsfwDetectorUrl"`
	// NSFWDetectorAuthHeader は detector への 1 行 HTTP header。例:
	// "Authorization: Bearer xxxxx" / "X-API-Key: xxxxx"。SaaS detector
	// (Cloudflare Workers AI / AWS Rekognition 等) を thin proxy なしで
	// 直接呼ぶための設定 (#751)。空なら header 注入なし。
	NSFWDetectorAuthHeader string `mapstructure:"nsfwDetectorAuthHeader"`
	// NSFWDetectorTimeout は detector への 1 リクエストあたりの timeout
	// (Go duration、例: "60s" / "2m")。0 / 未指定なら 30s (#751)。
	NSFWDetectorTimeout time.Duration   `mapstructure:"nsfwDetectorTimeout"`
	Logging             *LoggingOptions `mapstructure:"logging"`

	PerChannelMaxNoteCacheCount  *int   `mapstructure:"perChannelMaxNoteCacheCount"`
	PerUserNotificationsMaxCount *int   `mapstructure:"perUserNotificationsMaxCount"`
	DeactivateAntennaThreshold   *int   `mapstructure:"deactivateAntennaThreshold"`
	PidFile                      string `mapstructure:"pidFile"`

	// TrustProxy is a list of CIDR ranges for trusted reverse proxies.
	// When set, Echo uses X-Forwarded-For from these ranges to determine
	// the real client IP. Defaults to private IP ranges (TS-compatible).
	TrustProxy []string `mapstructure:"trustProxy"`

	// TestMode enables destructive test-only endpoints such as /api/reset-db.
	// Must never be enabled in production. Can be overridden via MK_TESTMODE=1.
	TestMode bool `mapstructure:"testMode"`

	// EnablePprof registers net/http/pprof handlers at /debug/pprof/*.
	// For local profiling only; must not be enabled in production.
	EnablePprof bool `mapstructure:"enablePprof"`

	// EnableMetrics registers a Prometheus /metrics endpoint exposing the
	// job-queue metrics defined in internal/queue/metrics (worker count /
	// queue depth / scale events). Defaults to false. Operators that
	// scrape from grafana / HPA should set true and gate access via
	// nginx / load-balancer ACLs since the endpoint is unauthenticated.
	// See docs/design/auto-scale-job-workers.md §6.1 for the metric
	// catalog.
	EnableMetrics bool `mapstructure:"enableMetrics"`

	// SentryForBackend enables Sentry error tracking for the Go server. Nil
	// disables Sentry entirely (production default).
	SentryForBackend *SentryBackendOptions `mapstructure:"sentryForBackend"`

	// SentryForFrontend is captured for Misskey YAML compatibility but the Go
	// backend itself does not consume it; downstream API handlers may forward
	// it to the frontend bundle. map[string]any keeps the YAML shape as-is.
	SentryForFrontend map[string]any `mapstructure:"sentryForFrontend"`

	// PublishTarballInsteadOfProvideRepositoryUrl mirrors the upstream Misskey
	// YAML flag. When true, the frontend treats the release as a tarball
	// distribution rather than pointing at the source repository URL. Exposed
	// via /api/meta.providesTarball.
	PublishTarballInsteadOfProvideRepositoryUrl bool `mapstructure:"publishTarballInsteadOfProvideRepositoryUrl"`
}

// Config represents the resolved application configuration.
type Config struct {
	Version string
	URL     string
	Port    int
	// Socket, if non-empty, is a path to a UNIX domain socket that the HTTP
	// server listens on instead of the TCP port.
	Socket string
	// ChmodSocket is the mode (as a string like "770") applied to the UNIX
	// domain socket file after bind. Ignored when Socket is empty or when
	// the value cannot be parsed as octal.
	ChmodSocket string
	Host        string
	Hostname    string
	Scheme      string
	WsScheme    string
	WsURL       string
	APIURL      string
	AuthURL     string
	DriveURL    string

	DisableHSTS               bool
	EnableIPRateLimit         bool
	DisableEndpointRateLimits bool
	SetupPassword             string

	DB             DBOptions
	DBReplications bool
	DBSlaves       []DBSlaveOptions

	Redis             RedisOptions
	RedisForPubsub    RedisOptions
	RedisForJobQueue  RedisOptions
	RedisForTimelines RedisOptions
	RedisForReactions RedisOptions

	FulltextSearch *FulltextSearchOptions
	Meilisearch    *MeilisearchOptions

	ID string

	Proxy                  string
	ProxySmtp              string
	ProxyBypassHosts       []string
	AllowedPrivateNetworks []string

	MaxFileSize           int64
	ClusterLimit          *int
	OutgoingAddress       string
	OutgoingAddressFamily string

	DeliverJobConcurrency      *int
	InboxJobConcurrency        *int
	RelationshipJobConcurrency *int
	DeliverJobPerSec           *int
	InboxJobPerSec             *int
	RelationshipJobPerSec      *int
	DeliverJobMaxAttempts      *int
	InboxJobMaxAttempts        *int
	DeliverJobKeepFailed       *int
	InboxJobKeepFailed         *int
	DeliverJobKeepCompleted    *int
	InboxJobKeepCompleted      *int

	// Auto-scale knobs (#1120 tracker). See docs/design/auto-scale-job-workers.md.
	JobQueueAutoScale        bool
	MaxWorkers               *int
	MinWorkers               *int
	MaxWorkersGlobal         *int
	AutoScaleCooldownSeconds *int

	// JobQueueDriver is one of "asynq" (default) or "mkq".
	JobQueueDriver string

	MediaProxy                   string
	ExternalMediaProxyEnabled    bool
	MediaProxySecret             []byte
	VideoThumbnailGenerator      string
	VideoThumbnailGeneratorMode  string
	NSFWDetectorURL              string
	NSFWDetectorAuthHeader       string
	NSFWDetectorTimeout          time.Duration
	UserAgent                    string
	PerChannelMaxNoteCacheCount  int
	PerUserNotificationsMaxCount int
	DeactivateAntennaThreshold   int
	PidFile                      string
	Logging                      *LoggingOptions

	// TrustProxy is a list of CIDR ranges for trusted reverse proxies.
	TrustProxy []string

	// TestMode enables destructive test-only endpoints such as /api/reset-db.
	// Must never be enabled in production. Can be overridden via MK_TESTMODE=1.
	TestMode bool

	// EnablePprof registers net/http/pprof handlers at /debug/pprof/*.
	EnablePprof bool

	// EnableMetrics registers the Prometheus /metrics endpoint. See
	// internal/queue/metrics for the exposed metric catalog.
	EnableMetrics bool

	// SentryForBackend, when non-nil, enables Sentry error tracking for the
	// Go server.
	SentryForBackend *SentryBackendOptions

	// SentryForFrontend is forwarded as-is to clients that need it (parsed
	// for YAML compatibility; the Go backend itself does not consume it).
	SentryForFrontend map[string]any

	// PublishTarballInsteadOfProvideRepositoryUrl is exposed to clients via
	// /api/meta.providesTarball. See the Source struct for details.
	PublishTarballInsteadOfProvideRepositoryUrl bool
}

const defaultMaxFileSize int64 = 262144000

// Load reads the Misskey YAML configuration and returns a resolved Config.
// Environment variables with the prefix MK_ override YAML values.
// Nested keys use underscore separation (e.g. MK_DB_HOST overrides db.host).
func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// 環境変数によるオーバーライド
	v.SetEnvPrefix("MK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 主要な設定キーを明示的にバインド（Viperは既知のキーのみ環境変数を適用する）
	bindEnvKeys(v)

	var src Source
	if err := v.Unmarshal(&src); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return resolve(&src)
}

// bindEnvKeys binds environment variables to known configuration keys.
func bindEnvKeys(v *viper.Viper) {
	keys := []string{
		"url", "port", "socket",
		"db.host", "db.port", "db.db", "db.user", "db.pass",
		"redis.host", "redis.path", "redis.port", "redis.pass", "redis.db", "redis.username",
		"redis.family", "redis.prefix",
		"redisForPubsub.host", "redisForPubsub.path", "redisForPubsub.port", "redisForPubsub.pass",
		"redisForPubsub.family", "redisForPubsub.prefix", "redisForPubsub.db", "redisForPubsub.username",
		"redisForJobQueue.host", "redisForJobQueue.path", "redisForJobQueue.port", "redisForJobQueue.pass",
		"redisForJobQueue.family", "redisForJobQueue.prefix", "redisForJobQueue.db", "redisForJobQueue.username",
		"redisForTimelines.host", "redisForTimelines.path", "redisForTimelines.port", "redisForTimelines.pass",
		"redisForTimelines.family", "redisForTimelines.prefix", "redisForTimelines.db", "redisForTimelines.username",
		"redisForReactions.host", "redisForReactions.path", "redisForReactions.port", "redisForReactions.pass",
		"redisForReactions.family", "redisForReactions.prefix", "redisForReactions.db", "redisForReactions.username",
		"db.maxOpenConns", "db.maxIdleConns", "db.connMaxLifetime", "db.connMaxIdleTime",
		"redis.poolSize",
		"redisForPubsub.poolSize",
		"redisForJobQueue.poolSize",
		"redisForTimelines.poolSize",
		"redisForReactions.poolSize",
		"id", "maxFileSize",
		"mediaProxySecret",
		"testMode",
		// 運用/セキュリティ系: 既存ymlから環境変数オーバーライドできるようにする
		"trustProxy",
		"disableHsts",
		"enableIpRateLimit",
		"disableEndpointRateLimits",
		"mediaProxy",
		"logging.sql.disableQueryTruncation",
		"logging.sql.enableQueryParamLogging",
		"enablePprof",
		"enableMetrics",
		"sentryForBackend.options.dsn",
		"sentryForBackend.options.environment",
		"jobQueueDriver",
		"jobQueueAutoScale",
		"maxWorkers",
		"minWorkers",
		"maxWorkersGlobal",
		"autoScaleCooldownSeconds",
	}
	for _, k := range keys {
		_ = v.BindEnv(k)
	}
}

func resolve(src *Source) (*Config, error) {
	parsedURL, err := url.Parse(src.URL)
	if err != nil || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", src.URL)
	}

	host := parsedURL.Host
	hostname := parsedURL.Hostname()
	scheme := strings.TrimSuffix(parsedURL.Scheme, ":")
	wsScheme := strings.Replace(scheme, "http", "ws", 1)

	maxFileSize := defaultMaxFileSize
	if src.MaxFileSize != nil {
		maxFileSize = *src.MaxFileSize
	}

	enableIPRateLimit := true
	if src.EnableIPRateLimit != nil {
		enableIPRateLimit = *src.EnableIPRateLimit
	}
	disableEndpointRateLimits := false
	if src.DisableEndpointRateLimits != nil {
		disableEndpointRateLimits = *src.DisableEndpointRateLimits
	}

	redis := resolveRedis(src.Redis, host)

	perChannelMaxNoteCacheCount := 1000
	if src.PerChannelMaxNoteCacheCount != nil {
		perChannelMaxNoteCacheCount = *src.PerChannelMaxNoteCacheCount
	}
	perUserNotificationsMaxCount := 500
	if src.PerUserNotificationsMaxCount != nil {
		perUserNotificationsMaxCount = *src.PerUserNotificationsMaxCount
	}
	deactivateAntennaThreshold := 1000 * 60 * 60 * 24 * 7
	if src.DeactivateAntennaThreshold != nil {
		deactivateAntennaThreshold = *src.DeactivateAntennaThreshold
	}

	internalMediaProxy := fmt.Sprintf("%s://%s/proxy", scheme, host)
	mediaProxy := internalMediaProxy
	externalMediaProxyEnabled := false
	if src.MediaProxy != "" {
		mp := strings.TrimRight(src.MediaProxy, "/")
		if mp != internalMediaProxy {
			externalMediaProxyEnabled = true
		}
		mediaProxy = mp
	}

	mediaProxySecret := deriveMediaProxySecret(src)

	jobQueueDriver, err := resolveJobQueueDriver(src.JobQueueDriver)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Version:     MisskeyVersion,
		URL:         parsedURL.Scheme + "://" + parsedURL.Host,
		Port:        src.Port,
		Socket:      src.Socket,
		ChmodSocket: src.ChmodSocket,
		Host:        host,
		Hostname:    hostname,
		Scheme:      scheme,
		WsScheme:    wsScheme,
		WsURL:       fmt.Sprintf("%s://%s", wsScheme, host),
		APIURL:      fmt.Sprintf("%s://%s/api", scheme, host),
		AuthURL:     fmt.Sprintf("%s://%s/auth", scheme, host),
		DriveURL:    fmt.Sprintf("%s://%s/files", scheme, host),

		DisableHSTS:               src.DisableHSTS,
		EnableIPRateLimit:         enableIPRateLimit,
		DisableEndpointRateLimits: disableEndpointRateLimits,
		SetupPassword:             src.SetupPassword,

		DB:             resolveDB(src.DB),
		DBReplications: src.DBReplications,
		DBSlaves:       resolveDBSlaves(src.DBSlaves),

		Redis:             redis,
		RedisForPubsub:    resolveRedisOrDefault(src.RedisForPubsub, redis, host),
		RedisForJobQueue:  resolveRedisOrDefault(src.RedisForJobQueue, redis, host),
		RedisForTimelines: resolveRedisOrDefault(src.RedisForTimelines, redis, host),
		RedisForReactions: resolveRedisOrDefault(src.RedisForReactions, redis, host),

		FulltextSearch: src.FulltextSearch,
		Meilisearch:    src.Meilisearch,

		ID: src.ID,

		Proxy:                  src.Proxy,
		ProxySmtp:              src.ProxySmtp,
		ProxyBypassHosts:       src.ProxyBypassHosts,
		AllowedPrivateNetworks: src.AllowedPrivateNetworks,

		MaxFileSize:           maxFileSize,
		ClusterLimit:          src.ClusterLimit,
		OutgoingAddress:       src.OutgoingAddress,
		OutgoingAddressFamily: src.OutgoingAddressFamily,

		DeliverJobConcurrency:      src.DeliverJobConcurrency,
		InboxJobConcurrency:        src.InboxJobConcurrency,
		RelationshipJobConcurrency: src.RelationshipJobConcurrency,
		DeliverJobPerSec:           src.DeliverJobPerSec,
		InboxJobPerSec:             src.InboxJobPerSec,
		RelationshipJobPerSec:      src.RelationshipJobPerSec,
		DeliverJobMaxAttempts:      src.DeliverJobMaxAttempts,
		InboxJobMaxAttempts:        src.InboxJobMaxAttempts,
		DeliverJobKeepFailed:       src.DeliverJobKeepFailed,
		InboxJobKeepFailed:         src.InboxJobKeepFailed,
		DeliverJobKeepCompleted:    src.DeliverJobKeepCompleted,
		InboxJobKeepCompleted:      src.InboxJobKeepCompleted,
		JobQueueAutoScale:          src.JobQueueAutoScale,
		MaxWorkers:                 src.MaxWorkers,
		MinWorkers:                 src.MinWorkers,
		MaxWorkersGlobal:           src.MaxWorkersGlobal,
		AutoScaleCooldownSeconds:   src.AutoScaleCooldownSeconds,
		JobQueueDriver:             jobQueueDriver,

		MediaProxy:                   mediaProxy,
		ExternalMediaProxyEnabled:    externalMediaProxyEnabled,
		MediaProxySecret:             mediaProxySecret,
		VideoThumbnailGenerator:      strings.TrimRight(src.VideoThumbnailGenerator, "/"),
		VideoThumbnailGeneratorMode:  normalizeVideoThumbMode(src.VideoThumbnailGeneratorMode),
		NSFWDetectorURL:              strings.TrimRight(src.NSFWDetectorURL, "/"),
		NSFWDetectorAuthHeader:       src.NSFWDetectorAuthHeader,
		NSFWDetectorTimeout:          src.NSFWDetectorTimeout,
		UserAgent:                    fmt.Sprintf("mk-go/%s (%s)", MkGoVersion, src.URL),
		PerChannelMaxNoteCacheCount:  perChannelMaxNoteCacheCount,
		PerUserNotificationsMaxCount: perUserNotificationsMaxCount,
		DeactivateAntennaThreshold:   deactivateAntennaThreshold,
		PidFile:                      src.PidFile,
		Logging:                      src.Logging,

		TrustProxy: resolveTrustProxy(src.TrustProxy),

		TestMode:      src.TestMode,
		EnablePprof:   src.EnablePprof,
		EnableMetrics: src.EnableMetrics,

		SentryForBackend:  src.SentryForBackend,
		SentryForFrontend: src.SentryForFrontend,

		PublishTarballInsteadOfProvideRepositoryUrl: src.PublishTarballInsteadOfProvideRepositoryUrl,
	}

	if cfg.TestMode {
		// TestMode は /api/reset-db のような破壊的エンドポイントを有効化する。
		// 本番で誤って有効化されていないか気付けるよう、起動時に強く警告する。
		slog.Warn("config: TestMode is enabled; destructive test endpoints (e.g. /api/reset-db) are active. DO NOT enable this in production.",
			"url", cfg.URL)
	}

	if cfg.EnablePprof {
		slog.Warn("config: EnablePprof is enabled; /debug/pprof/* endpoints expose runtime internals. DO NOT enable this in production.",
			"url", cfg.URL)
	}

	if cfg.DisableEndpointRateLimits {
		// per-endpoint rate limit table を完全に無効化する flag。bench /
		// test でしか使わない想定のため、production で有効化されていないか
		// 気付けるよう強く警告する (TestMode / EnablePprof と同じ位置に
		// まとめる、router 側でなく resolve 側で出すことで全 entrypoint
		// を確実にカバー)。
		slog.Warn("config: DisableEndpointRateLimits is enabled; per-endpoint rate limits will not enforce. DO NOT enable this in production.",
			"url", cfg.URL)
	}

	// HTTP socket と TCP port は両立しない。両方設定されていた場合は
	// socket を優先する旨警告ログを出し、運用者に気付けるようにする。
	if cfg.Socket != "" && cfg.Port != 0 {
		slog.Warn("config: both socket and port are set; socket takes precedence and port will be ignored",
			"socket", cfg.Socket, "port", cfg.Port)
	}

	return cfg, nil
}

func resolveDB(opts DBOptions) DBOptions {
	if opts.Port == 0 {
		opts.Port = 5432
	}
	return opts
}

func resolveDBSlaves(slaves []DBSlaveOptions) []DBSlaveOptions {
	for i := range slaves {
		if slaves[i].Port == 0 {
			slaves[i].Port = 5432
		}
	}
	return slaves
}

func resolveRedis(opts RedisOptions, host string) RedisOptions {
	if opts.Prefix == "" {
		opts.Prefix = host
	}
	return opts
}

// KeyPrefix returns the Redis key prefix to prepend to bare keys so that
// mk-go shares the same namespace as Misskey TS (TS uses ioredis `keyPrefix:
// <host>:` which gets applied to every key transparently). Empty Prefix
// returns "" for backwards compat with keys that were never prefixed.
//
// Consumers should prepend this to every Redis key they construct, e.g.
// `cfg.Redis.KeyPrefix() + "list:homeTimeline:" + userID`.
func (r RedisOptions) KeyPrefix() string {
	if r.Prefix == "" {
		return ""
	}
	return r.Prefix + ":"
}

// DefaultTrustProxy is the default set of CIDR ranges for trusted proxies,
// matching the TypeScript Misskey defaults (private IP ranges).
var DefaultTrustProxy = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"127.0.0.1/32",
	"::1/128",
	"fc00::/7",
}

// resolveTrustProxy returns the provided list or the default if empty.
func resolveTrustProxy(provided []string) []string {
	if len(provided) > 0 {
		return provided
	}
	return DefaultTrustProxy
}

// ParseTrustProxy converts a list of CIDR strings into parsed *net.IPNet values.
// Invalid CIDRs are skipped with a warning log.
func ParseTrustProxy(cidrs []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Warn("invalid trustProxy CIDR, skipping", "cidr", cidr, "err", err)
			continue
		}
		nets = append(nets, ipNet)
	}
	return nets
}

// deriveMediaProxySecret returns a secret for HMAC-signed media proxy URLs.
// 設定にsecretがあればそれを使用、なければインスタンスURL固有のキーを自動生成する。
func deriveMediaProxySecret(src *Source) []byte {
	if src.MediaProxySecret != "" {
		return []byte(src.MediaProxySecret)
	}
	// URLは公開情報なのでsecretとしては弱い。運用者に設定を促す。
	slog.Warn("config: mediaProxySecret is not set; using a URL-derived fallback. Set mediaProxySecret in config for stronger HMAC security.")
	h := sha256.Sum256([]byte(src.URL + "|mediaproxy"))
	return h[:]
}

func resolveRedisOrDefault(opts *RedisOptions, fallback RedisOptions, host string) RedisOptions {
	if opts == nil {
		return fallback
	}
	return resolveRedis(*opts, host)
}

// resolveJobQueueDriver normalises the jobQueueDriver config value.
// Empty / whitespace falls back to "mkq" (recommended driver、#571 audit)。
// Unknown non-empty values return an error: silently swapping a typo
// (e.g. "mkqq") for the default hides operator intent, especially given
// that internal/server/queue_factory.go also rejects unknown driver
// names — surfacing the failure here keeps the two layers consistent
// and the boot log readable.
//
// asynq driver は legacy / future-deprecation candidate。mkq の安定性が
// 確保され次第削除予定なので、新規 deploy は明示的に "asynq" を選ぶ
// 必要はない。default が mkq に変更されたが、既存運用で "asynq" を明示
// 指定している operator は影響を受けない。
func resolveJobQueueDriver(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "":
		return "mkq", nil
	case "asynq", "mkq":
		return v, nil
	default:
		return "", fmt.Errorf("config: unknown jobQueueDriver %q (expected \"asynq\" or \"mkq\")", raw)
	}
}

// normalizeVideoThumbMode validates and lowercases the wire-mode hint for
// the video thumbnail generator. Unknown / empty defaults to "post"
// (nekonoverse/video-thumb compat). "get" picks the Misskey TS-style URL
// pattern.
func normalizeVideoThumbMode(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "get":
		return "get"
	default:
		return "post"
	}
}

// DSN returns the PostgreSQL connection string.
//
// Host が "/" で始まる場合は UNIX domain socket 接続とみなす。libpq / pgx の
// 慣例に従い、host にソケットディレクトリのパスを、port に対応する PG ポート番号
// (socket 名 .s.PGSQL.<port> の末尾数字) を渡す。UDS では TLS を張れないので
// sslmode は強制的に disable になる。
func (c *Config) DSN() string {
	if IsUnixSocketPath(c.DB.Host) {
		return fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			c.DB.Host, c.DB.Port, c.DB.User, c.DB.Pass, c.DB.DB,
		)
	}
	sslMode := "disable"
	if v, ok := c.DB.Extra["ssl"]; ok && v == "true" {
		sslMode = "require"
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.DB.Host, c.DB.Port, c.DB.User, c.DB.Pass, c.DB.DB, sslMode,
	)
}

// SlaveDSN returns the PostgreSQL connection string for the idx-th read
// replica declared in DBSlaves. idx が範囲外の場合は空文字列を返す。
//
// SSL/sslmode / Unix socket 判定は primary の DB 設定に合わせる。
// Misskey 本家では dbSlaves エントリ内で sslmode を個別指定する API は無いため、
// primary の extra.ssl を継承する (same-network / same-cluster replica を前提)。
func (c *Config) SlaveDSN(idx int) string {
	if idx < 0 || idx >= len(c.DBSlaves) {
		return ""
	}
	s := c.DBSlaves[idx]
	if IsUnixSocketPath(s.Host) {
		return fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
			s.Host, s.Port, s.User, s.Pass, s.DB,
		)
	}
	sslMode := "disable"
	if v, ok := c.DB.Extra["ssl"]; ok && v == "true" {
		sslMode = "require"
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		s.Host, s.Port, s.User, s.Pass, s.DB, sslMode,
	)
}

// IsUnixSocketPath reports whether the given host string points to a UNIX
// domain socket path. The convention is that any absolute path (starts with
// "/") is treated as a socket. Empty strings and TCP hostnames return false.
//
// This helper is shared between the DB DSN builder, the Redis client factory
// and the HTTP server startup path so that all three sub-systems agree on
// what counts as a UDS address.
func IsUnixSocketPath(host string) bool {
	return strings.HasPrefix(host, "/")
}
