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
	"errors"
	"fmt"
	"log/slog"
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
	// ClearScheduledNote は noteDraftId に該当する全 delayed task を queue
	// から削除する。upstream Misskey TS の `NoteDraftService.clearSchedule`
	// と同 pattern で線形探索 (= page-by-page で全 delayed task を走査し、
	// payload.NoteDraftID 一致を DeleteTask)。caller (NoteDraftService.update
	// / delete) が re-schedule / unschedule する際に呼ぶ (#1045 Phase 2-C)。
	ClearScheduledNote(draftID string) error
	// SupportsScheduledNote は driver が scheduled note 機能 (= delayed
	// enqueue + clearSchedule の確実な動作) を提供するかを返す。mk-go の
	// asynq driver は task ID 仕様の制約で確実な clearSchedule が困難な
	// ため false。mkq driver は true (#1045 Phase 2-C)。handler はこの
	// flag が false なら scheduled note 作成を `TOO_MANY_SCHEDULED_NOTES`
	// で拒否する。
	SupportsScheduledNote() bool
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
	// inspector は ClearScheduledNote 等の delayed task 走査経路で使う
	// (#1045 Phase 2-C)。enqueue 経路は inner.Client のみ使用、inspector
	// は lazy に Driver から取得しキャッシュする。
	inspector driver.Inspector
	// supportsScheduledNote は scheduled note 機能が確実に動作する driver
	// (= mkq) で wire 時に true、それ以外 (= asynq) は false。handler の
	// scheduled note 経路で capability gate として参照する (#1045 Phase 2-C)。
	supportsScheduledNote bool

	mu       sync.RWMutex
	policies PolicyMap
}

// NewClient constructs a Client backed by the supplied driver.
func NewClient(d driver.Driver) *Client {
	return &Client{
		inner:     d.Client(),
		inspector: d.Inspector(),
	}
}

// SetSupportsScheduledNote toggles the driver capability flag exposed via
// `SupportsScheduledNote`. wire 時に driver kind を見て router で設定する
// (= mkq → true, asynq → false)。default は false (= 安全側)。
func (c *Client) SetSupportsScheduledNote(b bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.supportsScheduledNote = b
}

// SupportsScheduledNote returns the capability flag. handler はこの flag が
// false なら scheduled note 作成を `TOO_MANY_SCHEDULED_NOTES` で拒否する。
func (c *Client) SupportsScheduledNote() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.supportsScheduledNote
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

// retentionOpts returns the driver-neutral options derived from the
// queue's Policy (KeepCompleted / KeepCompletedAge / KeepFailed /
// KeepFailedAge). All Enqueue* helpers pre-pend this output so the
// BullMQ-compatible removeOnComplete / removeOnFail are wired uniformly
// across deliver / inbox / export / push / webhook (#1193)。
//
// 自前で Policy を引いていない caller 向けの薄い wrapper。MaxAttempts も
// 見たい caller は policyFor を呼んでから retentionOptsFromPolicy を直接
// 使うことで RLock を 1 回に抑えられる (EnqueueDeliver / EnqueueInbox 経路)。
func (c *Client) retentionOpts(queueName string) []driver.EnqueueOption {
	return retentionOptsFromPolicy(c.policyFor(queueName))
}

// retentionOptsFromPolicy is the lock-free core of retentionOpts. 各 field は
// 0 (= unset) のとき option を emit しない (driver default = unlimited retention)
// ように `> 0` で guard。これは driver-neutral layer の `WithKeepFailed` /
// `WithKeepCompleted` の 0-skip semantic と同じ規約 (mkqdriver/option.go の
// 対応 guard 参照)。
func retentionOptsFromPolicy(p Policy) []driver.EnqueueOption {
	out := make([]driver.EnqueueOption, 0, 4)
	if p.KeepCompleted > 0 {
		out = append(out, driver.WithKeepCompleted(p.KeepCompleted))
	}
	if p.KeepCompletedAge > 0 {
		out = append(out, driver.WithKeepCompletedAge(p.KeepCompletedAge))
	}
	if p.KeepFailed > 0 {
		out = append(out, driver.WithKeepFailed(p.KeepFailed))
	}
	if p.KeepFailedAge > 0 {
		out = append(out, driver.WithKeepFailedAge(p.KeepFailedAge))
	}
	return out
}

// backoffOptFromPolicy returns the retry backoff option for a federation
// queue. Policy override が指定されていればそれを使い、未指定なら deliver /
// inbox の既定 (= Misskey TS httpRelatedBackoff の custom strategy) に
// フォールバックする (#1405 / #1406, mkq#67)。custom は delay 不要なので
// BackoffType=="custom" 単体で override 成立、built-in は delay>0 も要求する。
func backoffOptFromPolicy(p Policy) driver.EnqueueOption {
	switch {
	case p.BackoffType == driver.BackoffCustom:
		return driver.WithBackoff(driver.BackoffCustom, 0)
	case p.BackoffType != "" && p.BackoffDelay > 0:
		return driver.WithBackoff(p.BackoffType, p.BackoffDelay)
	case p.BackoffType != "":
		// built-in backoff (fixed/exponential) は delay が必須なのに未指定。
		// delay=0 のまま当てるとリトライが遅延0で即時連打になり危険なので、
		// 設定ミスを警告したうえで federation 既定 backoff にフォールバックする (#1409)。
		slog.Warn("queue: built-in backoff type specified without a positive delay; falling back to federation backoff",
			"backoffType", p.BackoffType, "backoffDelay", p.BackoffDelay)
		return driver.WithFederationBackoff()
	default:
		return driver.WithFederationBackoff()
	}
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
	// 1 回の policyFor で MaxAttempts と retention の両方を済ませる
	// (RLock 1 回 / map lookup 1 回に集約)。
	p := c.policyFor(QueueName)
	base := []driver.EnqueueOption{driver.WithQueue(QueueName)}
	if p.MaxAttempts > 0 {
		base = append(base, driver.WithMaxRetry(p.MaxAttempts-1))
	}
	// 失敗時は exponential backoff で再試行する。backoff が無いと mkq は
	// 遅延0で即再 enqueue し、落ちている配送先を連打 + delayed に滞在しない
	// ため admin の Delayed が常に空に見える。Policy で上書き可 (#1405 / #1406)。
	base = append(base, backoffOptFromPolicy(p))
	// completed / failed bucket retention を Policy から組み立てる (#1184 / #1193)。
	// mkqdriver 経路でのみ効き、asynqdriver では silent no-op。
	base = append(base, retentionOptsFromPolicy(p)...)
	merged := append(base, opts...)
	return c.inner.Enqueue(context.Background(), TaskTypeDeliver, body, merged...)
}

// EnqueuePostScheduledNote enqueues a deferred publish task for a draft
// flagged with `isActuallyScheduled=true`. caller は WithProcessIn(delay)
// で `scheduledAt - now` を指定する想定 (#1040)。
func (c *Client) EnqueuePostScheduledNote(payload PostScheduledNotePayload, opts ...driver.EnqueueOption) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(QueueName)}
	base = append(base, c.retentionOpts(QueueName)...)
	merged := append(base, opts...)
	return c.inner.Enqueue(context.Background(), TaskTypePostScheduledNote, body, merged...)
}

// clearScheduledNotePageSize は ClearScheduledNote の line scan で 1 度に
// fetch する task 件数。inspector の page 単位走査で memory pressure を
// 抑えつつ大量 delayed task の場合も全件走査できる pragmatic な閾値。
const clearScheduledNotePageSize = 100

// ClearScheduledNote scans the deliver queue's delayed bucket for tasks
// matching noteDraftId == draftID and deletes them. inspector が未配線の
// 場合は no-op (= test fixture / wire 未配線 path)。upstream の線形探索
// と同 pattern (`getJobs(['delayed', 'waiting', 'active'])` → match →
// `job.remove()`) を asynq/mkq の inspector API で再現する。
//
// 削除 failure は集約して error として返す。partial success 時も err 経路
// に流すことで caller (DraftsUpdate / DraftsDelete) が log を残し handler
// 戻り値で 500 を返せるようにする。
func (c *Client) ClearScheduledNote(draftID string) error {
	if c.inspector == nil {
		return nil
	}
	page := 1
	var firstErr error
	for {
		tasks, err := c.inspector.ListScheduledTasks(QueueName, page, clearScheduledNotePageSize)
		if err != nil {
			return fmt.Errorf("list scheduled tasks: %w", err)
		}
		if len(tasks) == 0 {
			break
		}
		for _, t := range tasks {
			if t.Type != TaskTypePostScheduledNote {
				continue
			}
			payload, derr := DecodePostScheduledNotePayload(t.Payload)
			if derr != nil {
				// 破損 payload は skip (caller の問題ではなく、開発時に
				// payload 型を変えた際の遷移期 noise を握り潰す)。
				continue
			}
			if payload.NoteDraftID != draftID {
				continue
			}
			if derr := c.inspector.DeleteTask(QueueName, t.ID); derr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete task %s: %w", t.ID, derr)
				}
			}
		}
		if len(tasks) < clearScheduledNotePageSize {
			break
		}
		page++
	}
	return firstErr
}

// EnqueueExport puts an export task on the queue.
func (c *Client) EnqueueExport(payload ExportPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(ExportQueueName)}
	base = append(base, c.retentionOpts(ExportQueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeExport, body, base...)
}

// EnqueueImport puts an import task on the queue.
func (c *Client) EnqueueImport(payload ImportPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(ExportQueueName)}
	base = append(base, c.retentionOpts(ExportQueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeImport, body, base...)
}

// EnqueueImportCustomEmojis puts an admin emoji-zip import task on the
// export queue. Misskey 本家も同じ "dbQueue" (低優先) に積んでいる。
func (c *Client) EnqueueImportCustomEmojis(payload ImportCustomEmojisPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(ExportQueueName)}
	base = append(base, c.retentionOpts(ExportQueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeImportCustomEmojis, body, base...)
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
	// EnqueueDeliver と同じ理由で 1-shot lookup (#1184 / #1193)。
	p := c.policyFor(InboxQueueName)
	base := []driver.EnqueueOption{driver.WithQueue(InboxQueueName)}
	if p.MaxAttempts > 0 {
		base = append(base, driver.WithMaxRetry(p.MaxAttempts-1))
	}
	// deliver と同じく exponential backoff で再試行する。Policy で上書き可
	// (#1405 / #1406)。
	base = append(base, backoffOptFromPolicy(p))
	base = append(base, retentionOptsFromPolicy(p)...)
	return c.inner.Enqueue(ctx, TaskTypeInbox, body, base...)
}

// EnqueueWebPush puts a Web Push delivery task on the push queue.
func (c *Client) EnqueueWebPush(ctx context.Context, payload WebPushPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{driver.WithQueue(PushQueueName)}
	base = append(base, c.retentionOpts(PushQueueName)...)
	return c.inner.Enqueue(ctx, TaskTypeWebPush, body, base...)
}

// EnqueueUserWebhook puts a user webhook delivery task on the webhook
// queue. Retry policy: 4 attempts (4xx は processor 側で SkipRetry と
// して扱うため実際のリトライ対象は 5xx とネットワークエラーに限られる)。
func (c *Client) EnqueueUserWebhook(ctx context.Context, payload WebhookPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{
		driver.WithQueue(WebhookQueueName),
		driver.WithMaxRetry(4),
		// deliver/inbox と同じ custom backoff を webhook 配送にも付与する。
		// worker 側 strategy は全 queue に登録済みなので enqueue 側で付けるだけで
		// 段階的なリトライ間隔が効く (#1408)。
		driver.WithFederationBackoff(),
	}
	base = append(base, c.retentionOpts(WebhookQueueName)...)
	return c.inner.Enqueue(ctx, TaskTypeUserWebhook, body, base...)
}

// EnqueueSystemWebhook puts a system webhook delivery task on the webhook queue.
func (c *Client) EnqueueSystemWebhook(ctx context.Context, payload WebhookPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{
		driver.WithQueue(WebhookQueueName),
		driver.WithMaxRetry(4),
		// user webhook と同様に custom backoff を付与する (#1408)。
		driver.WithFederationBackoff(),
	}
	base = append(base, c.retentionOpts(WebhookQueueName)...)
	return c.inner.Enqueue(ctx, TaskTypeSystemWebhook, body, base...)
}

// EnqueueCleanRemoteNotes puts a remote notes cleaning task on the queue.
// 重複排除のため UniqueFor を設定。
func (c *Client) EnqueueCleanRemoteNotes() error {
	base := []driver.EnqueueOption{
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(6 * time.Hour),
	}
	base = append(base, c.retentionOpts(QueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeCleanRemoteNotes, nil, base...)
}

// EnqueueReactionFlush puts a reaction flush task on the queue.
func (c *Client) EnqueueReactionFlush() error {
	base := []driver.EnqueueOption{
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(0),
		driver.WithUnique(25 * time.Second),
	}
	base = append(base, c.retentionOpts(QueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeReactionFlush, nil, base...)
}

// EnqueueDeleteAccount schedules a cascade deletion of the user's
// related rows. Uniqueness over a 24h window prevents duplicate jobs
// if the admin clicks delete multiple times while the previous run is
// still processing.
func (c *Client) EnqueueDeleteAccount(payload DeleteAccountPayload) error {
	body := mustMarshal(payload)
	base := []driver.EnqueueOption{
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(2),
		driver.WithUnique(24 * time.Hour),
	}
	base = append(base, c.retentionOpts(QueueName)...)
	return c.inner.Enqueue(context.Background(), TaskTypeDeleteAccount, body, base...)
}

// EnqueueUnfollow schedules a single Unfollow job for the given pair.
// admin/federation/remove-all-following が大量に enqueue する想定で、
// per-pair retry を独立に効かせるため Unique は付けない (同じペアの
// 重複 enqueue は worker 側で冪等吸収)。MaxRetry は federation hook の
// AP delivery 失敗を吸収できる程度に設定。
func (c *Client) EnqueueUnfollow(payload UnfollowPayload) error {
	body := mustMarshal(payload)
	return c.enqueueRelationship(TaskTypeUnfollow, body)
}

// EnqueueFollow schedules a single Follow job for the given pair.
// Misskey TS の relationshipQueue 'follow' job 相当 (#1605)。retry /
// uniqueness の方針は EnqueueUnfollow と同じ。
func (c *Client) EnqueueFollow(payload FollowPayload) error {
	body := mustMarshal(payload)
	return c.enqueueRelationship(TaskTypeFollow, body)
}

// EnqueueBlock schedules a single Block job for the given pair (#1605)。
func (c *Client) EnqueueBlock(payload BlockPayload) error {
	body := mustMarshal(payload)
	return c.enqueueRelationship(TaskTypeBlock, body)
}

// EnqueueUnblock schedules a single Unblock job for the given pair (#1605)。
func (c *Client) EnqueueUnblock(payload UnblockPayload) error {
	body := mustMarshal(payload)
	return c.enqueueRelationship(TaskTypeUnblock, body)
}

// enqueueRelationship is the shared enqueue path for the per-pair
// relationship jobs (follow / unfollow / block / unblock)。
func (c *Client) enqueueRelationship(taskType string, body []byte) error {
	base := []driver.EnqueueOption{
		driver.WithQueue(QueueName),
		driver.WithMaxRetry(3),
	}
	base = append(base, c.retentionOpts(QueueName)...)
	return c.inner.Enqueue(context.Background(), taskType, body, base...)
}

// EnqueueFollowBulk schedules one Follow job per payload. 本家
// QueueService.createFollowJob の addBulk 相当 (#1605)。driver に bulk
// API は無いため逐次 enqueue し、失敗した payload 分のエラーを
// errors.Join で集約して返す (成功分は enqueue 済のまま)。worker 側が
// 冪等吸収するため、エラー時に batch 全体を再実行しても安全。
func (c *Client) EnqueueFollowBulk(payloads []FollowPayload) error {
	var errs []error
	for _, p := range payloads {
		if err := c.EnqueueFollow(p); err != nil {
			errs = append(errs, fmt.Errorf("follow %s->%s: %w", p.FollowerID, p.FolloweeID, err))
		}
	}
	return errors.Join(errs...)
}

// EnqueueUnfollowBulk schedules one Unfollow job per payload. 本家
// QueueService.createUnfollowJob の addBulk 相当 (#1605)。
func (c *Client) EnqueueUnfollowBulk(payloads []UnfollowPayload) error {
	var errs []error
	for _, p := range payloads {
		if err := c.EnqueueUnfollow(p); err != nil {
			errs = append(errs, fmt.Errorf("unfollow %s->%s: %w", p.FollowerID, p.FolloweeID, err))
		}
	}
	return errors.Join(errs...)
}

// EnqueueBlockBulk schedules one Block job per payload. 本家
// QueueService.createBlockJob の addBulk 相当 (#1605)。
func (c *Client) EnqueueBlockBulk(payloads []BlockPayload) error {
	var errs []error
	for _, p := range payloads {
		if err := c.EnqueueBlock(p); err != nil {
			errs = append(errs, fmt.Errorf("block %s->%s: %w", p.BlockerID, p.BlockeeID, err))
		}
	}
	return errors.Join(errs...)
}

// EnqueueUnblockBulk schedules one Unblock job per payload. 本家
// QueueService.createUnblockJob の addBulk 相当 (#1605)。
func (c *Client) EnqueueUnblockBulk(payloads []UnblockPayload) error {
	var errs []error
	for _, p := range payloads {
		if err := c.EnqueueUnblock(p); err != nil {
			errs = append(errs, fmt.Errorf("unblock %s->%s: %w", p.BlockerID, p.BlockeeID, err))
		}
	}
	return errors.Join(errs...)
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
