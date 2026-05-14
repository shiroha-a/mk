// Package queue is the driver-neutral facade for mk-go's job queue.
// Callers depend on this package (Enqueuer, Server, Inspector,
// Scheduler) without taking a compile-time dependency on the
// underlying queue runtime — that lives behind queue/driver.
//
// AP delivery, webhooks, web push, and maintenance / chart cron
// jobs all flow through this package. Driver swaps (asynq → mkq)
// touch only the wiring code that constructs the driver.Driver in
// internal/server.
package queue

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// mustMarshal serializes a payload via json.Marshal. mk-go の queue
// payload は string / []byte / 単純な struct のみで構成されている
// ため Marshal は失敗しない。エラー戻り値を呼び出し側に伝播させる
// より panic で wiring バグを早期発見できる方を選んでいる。
func mustMarshal(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic("queue: marshal payload: " + err.Error())
	}
	return body
}

// QueueName is the queue used for AP delivery jobs.
const QueueName = "deliver"

// ExportQueueName is the queue for export/import jobs.
const ExportQueueName = "export"

// PushQueueName is the queue for Web Push delivery jobs.
const PushQueueName = "push"

// WebhookQueueName is the queue for user + system webhook delivery jobs.
const WebhookQueueName = "webhook"

// InboxQueueName is the queue for inbound ActivityPub activity processing
// (#534). Misskey TS uses the same name (lower-case "inbox") for BullMQ
// drop-in compat.
const InboxQueueName = "inbox"

// Enqueuer abstracts task enqueueing for callers (DeliverService,
// admin handlers, etc.). The interface is driver-neutral so callers
// can be unit-tested with mocks.
type Enqueuer interface {
	EnqueueDeliver(payload DeliverPayload, opts ...driver.EnqueueOption) error
	EnqueueExport(payload ExportPayload) error
	EnqueueImport(payload ImportPayload) error
	EnqueueWebPush(ctx context.Context, payload WebPushPayload) error
	EnqueueUserWebhook(ctx context.Context, payload WebhookPayload) error
	EnqueueSystemWebhook(ctx context.Context, payload WebhookPayload) error
	EnqueueInbox(ctx context.Context, payload InboxPayload) error
	EnqueuePostScheduledNote(payload PostScheduledNotePayload, opts ...driver.EnqueueOption) error
	Close() error
}

// Client wraps a driver.Client and implements Enqueuer.
//
// 通常 SetPolicy は server 構築時 1 度だけ呼ばれるため概念上 race は無い
// が、SetPolicy が exported method として公開されていることから将来の
// foot-gun を避けるため policies map は sync.RWMutex で保護する (#531
// review)。read 経路 (EnqueueDeliver) は RLock のみで、token contention
// は実質ゼロ。
type Client struct {
	inner driver.Client

	mu       sync.RWMutex
	policies PolicyMap
}

// NewClient constructs a Client backed by the supplied driver.
func NewClient(d driver.Driver) *Client {
	return &Client{inner: d.Client()}
}

// SetPolicy registers a runtime Policy for queueName. EnqueueDeliver
// consults this when the caller doesn't specify WithMaxRetry. Subsequent
// calls overwrite any prior policy for the same queue.
func (c *Client) SetPolicy(queueName string, p Policy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.policies == nil {
		c.policies = make(PolicyMap)
	}
	c.policies[queueName] = p
}

// policyFor returns the Policy for queueName under RLock. Falls back to the
// zero Policy when no entry is registered (PolicyMap.PolicyFor も nil-safe
// だが、ここで lock を取り抜いてから map lookup する必要がある)。
func (c *Client) policyFor(queueName string) Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.policies.PolicyFor(queueName)
}

// EnqueueDeliver puts a deliver task on the queue. opts override the
// default queue selection if they include WithQueue, but normal
// callers should pass payload-only and let the queue routing stay
// fixed.
//
// 当該キューの Policy.MaxAttempts が 0 でなければ、WithMaxRetry を
// caller opts より先に積んで default として扱う。caller が
// WithMaxRetry を渡したときは ApplyEnqueueOptions の last-write-wins で
// caller 側が勝つ (#495)。
//
// MaxAttempts は BullMQ semantics の総試行回数 (TS Misskey YAML 互換)。
// driver.WithMaxRetry は「初回 + N 回 retry」の N なので N-1 を渡す。
// MaxAttempts=1 なら WithMaxRetry(0) = 「retry なし、初回のみ」になる
// (#531 review)。
func (c *Client) EnqueueDeliver(payload DeliverPayload, opts ...driver.EnqueueOption) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(QueueName)}
	if attempts := c.policyFor(QueueName).MaxAttempts; attempts > 0 {
		base = append(base, driver.WithMaxRetry(attempts-1))
	}
	merged := append(base, opts...)
	return c.inner.Enqueue(context.Background(), TaskTypeDeliver, body, merged...)
}

// EnqueuePostScheduledNote enqueues a deferred publish task for a draft
// flagged with `isActuallyScheduled=true`. caller は WithProcessIn(delay)
// で `scheduledAt - now` を指定する想定 (#1040)。
func (c *Client) EnqueuePostScheduledNote(payload PostScheduledNotePayload, opts ...driver.EnqueueOption) error {
	body := mustMarshal(payload)
	merged := append([]driver.EnqueueOption{driver.WithQueue(QueueName)}, opts...)
	return c.inner.Enqueue(context.Background(), TaskTypePostScheduledNote, body, merged...)
}

// EnqueueExport puts an export task on the queue.
func (c *Client) EnqueueExport(payload ExportPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(context.Background(), TaskTypeExport, body,
		driver.WithQueue(ExportQueueName),
	)
}

// EnqueueImport puts an import task on the queue.
func (c *Client) EnqueueImport(payload ImportPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(context.Background(), TaskTypeImport, body,
		driver.WithQueue(ExportQueueName),
	)
}

// EnqueueImportCustomEmojis puts an admin emoji-zip import task on the
// export queue. Misskey 本家も同じ "dbQueue" (低優先) に積んでいる。
func (c *Client) EnqueueImportCustomEmojis(payload ImportCustomEmojisPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(context.Background(), TaskTypeImportCustomEmojis, body,
		driver.WithQueue(ExportQueueName),
	)
}

// EnqueueInbox puts an inbound ActivityPub activity onto the inbox queue
// (#534). HTTP handler 側で signature 検証 + host block + chart hook を
// 同期で済ませた後、重い Process(body) だけを worker に逃がすために使う。
//
// Policy.MaxAttempts (= inboxJobMaxAttempts と同じ BullMQ 規約) が
// セットされていれば WithMaxRetry に N-1 で適用される。EnqueueInbox は
// 現状 caller opts を受け取らない (inbox 経路は単一の固定 enqueue で
// 十分なため)。将来オプション渡しが必要になったら EnqueueDeliver と同じ
// variadic pattern に揃える。
func (c *Client) EnqueueInbox(ctx context.Context, payload InboxPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(InboxQueueName)}
	if attempts := c.policyFor(InboxQueueName).MaxAttempts; attempts > 0 {
		base = append(base, driver.WithMaxRetry(attempts-1))
	}
	return c.inner.Enqueue(ctx, TaskTypeInbox, body, base...)
}

// EnqueueWebPush puts a Web Push delivery task on the push queue.
func (c *Client) EnqueueWebPush(ctx context.Context, payload WebPushPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(ctx, TaskTypeWebPush, body,
		driver.WithQueue(PushQueueName),
	)
}

// EnqueueUserWebhook puts a user webhook delivery task on the webhook
// queue. Retry policy: 4 attempts (4xx は processor 側で SkipRetry と
// して扱うため実際のリトライ対象は 5xx とネットワークエラーに限られる)。
func (c *Client) EnqueueUserWebhook(ctx context.Context, payload WebhookPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(ctx, TaskTypeUserWebhook, body,
		driver.WithQueue(WebhookQueueName),
		driver.WithMaxRetry(4),
	)
}

// EnqueueSystemWebhook puts a system webhook delivery task on the webhook queue.
func (c *Client) EnqueueSystemWebhook(ctx context.Context, payload WebhookPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(ctx, TaskTypeSystemWebhook, body,
		driver.WithQueue(WebhookQueueName),
		driver.WithMaxRetry(4),
	)
}

// EnqueueCleanRemoteNotes puts a remote notes cleaning task on the queue.
// 重複排除のため UniqueFor を設定。
func (c *Client) EnqueueCleanRemoteNotes() error {
	return c.inner.Enqueue(context.Background(), TaskTypeCleanRemoteNotes, nil,
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(6*time.Hour),
	)
}

// EnqueueReactionFlush puts a reaction flush task on the queue.
func (c *Client) EnqueueReactionFlush() error {
	return c.inner.Enqueue(context.Background(), TaskTypeReactionFlush, nil,
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(25*time.Second),
	)
}

// EnqueueDeleteAccount schedules a cascade deletion of the user's
// related rows. Uniqueness over a 24h window prevents duplicate jobs
// if the admin clicks delete multiple times while the previous run is
// still processing.
func (c *Client) EnqueueDeleteAccount(payload DeleteAccountPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(context.Background(), TaskTypeDeleteAccount, body,
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(2),
		driver.WithUnique(24*time.Hour),
	)
}

// EnqueueUnfollow schedules a single Unfollow job for the given pair.
// admin/federation/remove-all-following が大量に enqueue する想定で、
// per-pair retry を独立に効かせるため Unique は付けない (同じペアの
// 重複 enqueue は worker 側で冪等吸収)。MaxRetry は federation hook の
// AP delivery 失敗を吸収できる程度に設定。
func (c *Client) EnqueueUnfollow(payload UnfollowPayload) error {
	body := mustMarshal(payload)
	return c.inner.Enqueue(context.Background(), TaskTypeUnfollow, body,
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(3),
	)
}

// Close releases the underlying client connection.
func (c *Client) Close() error { return c.inner.Close() }

// Server is the worker side facade. It registers HandlerFuncs by
// task type and starts/stops the worker loop.
type Server struct {
	inner driver.Server
}

// ServerConfig is kept for backward compatibility with callers that
// pass a Concurrency value via internal/server. The driver itself
// gets its own concrete config (e.g. asynqdriver.ServerConfig)
// at construction time.
type ServerConfig struct {
	Concurrency int
}

// NewServer wraps the driver's Server. The driver must already be
// configured with the desired concurrency / queue weights.
func NewServer(d driver.Driver) *Server {
	return &Server{inner: d.Server()}
}

// Handle registers a handler for the given task type.
func (s *Server) Handle(taskType string, handler driver.HandlerFunc) {
	s.inner.Handle(taskType, handler)
}

// Start launches the worker in the background.
func (s *Server) Start() error { return s.inner.Start() }

// Shutdown gracefully stops the worker, waiting for in-flight jobs to finish.
func (s *Server) Shutdown() { s.inner.Shutdown() }
