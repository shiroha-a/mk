package queue

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/queue/driver"
)

// TaskTypeDeliver is the task type used for outbound ActivityPub
// delivery jobs.
const TaskTypeDeliver = "ap:deliver"

// TaskTypeExport is the task type for data export jobs.
const TaskTypeExport = "export"

// TaskTypeImport is the task type for data import jobs.
const TaskTypeImport = "import"

// TaskTypeWebPush is the task type for Web Push delivery jobs.
const TaskTypeWebPush = "webpush:notify"

// TaskTypeImportCustomEmojis is the task type for admin/emoji/import-zip
// processing jobs. 本家 Misskey の importCustomEmojis queue job と同名。
const TaskTypeImportCustomEmojis = "importCustomEmojis"

// TaskTypeUserWebhook is the task type for user webhook delivery jobs.
const TaskTypeUserWebhook = "webhook:user"

// TaskTypeSystemWebhook is the task type for system webhook delivery jobs.
const TaskTypeSystemWebhook = "webhook:system"

// TaskTypeCleanRemoteNotes is the task type for the periodic remote
// notes cleaning job. ペイロードなし (meta から設定を読む)。
const TaskTypeCleanRemoteNotes = "maintenance:cleanRemoteNotes"

// TaskTypeReactionFlush is the task type for flushing buffered
// reaction counts from Redis to the database.
const TaskTypeReactionFlush = "maintenance:reactionFlush"

// TaskTypeChartTick is the task type for the hourly tick-charts job.
// Mirrors upstream `tickCharts` (cron `55 * * * *`).
const TaskTypeChartTick = "chart:tick"

// TaskTypeChartResync is the task type for the daily resync-charts job.
// Mirrors upstream `resyncCharts` (cron `0 0 * * *`).
const TaskTypeChartResync = "chart:resync"

// TaskTypeChartClean is the task type for the daily clean-charts job.
// Mirrors upstream `cleanCharts` (cron `0 0 * * *`).
const TaskTypeChartClean = "chart:clean"

// TaskTypeCheckExpiredMutings is the task type for the periodic expired-mute
// pruning job. Mirrors upstream `checkExpiredMutings` (cron `*/5 * * * *`).
// 期限切れ user muting 行を能動削除する (read filter で既に隠れているが DB に
// 残るため hygiene)。channel-mute erase は mk-go に expiresAt 列が無いため別 issue。
const TaskTypeCheckExpiredMutings = "maintenance:checkExpiredMutings"

// TaskTypeClean is the task type for the daily generic clean job. Mirrors
// upstream `clean` (cron `0 0 * * *`)。user_ip の 90 日 prune / 期限切れ
// role_assignment 削除 / reversi outdated game 削除を行う (antenna deactivate は
// lastUsedAt refresh が無い mk-go では誤作動するため別 issue)。
const TaskTypeClean = "maintenance:clean"

// TaskTypeCheckModeratorsActivity is the task type for the hourly moderator
// activity check. Mirrors upstream `checkModeratorsActivity` (cron `30 * * * *`)。
// モデレーター全員が 7 日非アクティブだと登録を招待制に自動切替し、残 2 日では
// 6h ごとに警告メール / announcement / SystemWebhook を送る。
const TaskTypeCheckModeratorsActivity = "maintenance:checkModeratorsActivity"

// TaskTypeDeleteAccount is the task type for the background cascade
// deletion of a user account's related rows. Mirrors upstream
// `deleteAccount` queue job.
const TaskTypeDeleteAccount = "maintenance:deleteAccount"

// TaskTypeInstanceRefresh is the task type for the periodic
// remote-instance metadata refresh (#393). Registered by
// `Scheduler.RegisterInstanceRefreshJob` at `0 3 * * *` UTC and handled by
// `processors.InstanceRefreshProcessor`.
const TaskTypeInstanceRefresh = "maintenance:instanceRefresh"

// TaskTypeRetentionAggregate is the task type for the daily retention
// aggregation (#421). Registered by `Scheduler.RegisterRetentionJob`
// at `0 0 * * *` UTC and handled by
// `processors.RetentionAggregateProcessor`.
const TaskTypeRetentionAggregate = "maintenance:retentionAggregate"

// TaskTypePostScheduledNote is the task type for publishing a scheduled note
// (= `note_draft` with `isActuallyScheduled=true`) once its `scheduledAt`
// time has arrived. Enqueued by `notes/drafts/create` with WithProcessIn
// (= scheduledAt - now) and consumed by
// `processors.PostScheduledNoteProcessor` (#1040)。
const TaskTypePostScheduledNote = "note:postScheduled"

// TaskTypeInbox is the task type for inbound ActivityPub activity
// processing (#534). Enqueued by the inbox HTTP handler after signature
// verification, and consumed by a worker that calls
// `federation.Processor.Process` to apply the activity. 各 activity
// handler は冪等であることが前提 (Misskey TS と同じ吸収戦略)。
const TaskTypeInbox = "ap:inbox"

// InboxPayload is the body of an inbox task. Body は受信した raw AP
// activity の bytes そのまま (Process 側で再 normalize / decode する)。
//
// #565 で signature 検証 / host block / instance touch / chart hook を
// すべて worker 側に逃がす設計に変更した結果、payload は HTTP request の
// 復元に必要な Method / Path / Headers (Signature / Date / Digest /
// Host etc.) も運ぶ。Headers が non-nil の payload は worker 側で
// signature を再 verify する責務がある。
//
// Host fields はレガシー互換 (Headers が無い旧 payload では Host を
// 信頼してチャート / instance 更新する)。新 payload では Headers から
// 抽出するため通常空。
type InboxPayload struct {
	Body    []byte            `json:"body"`
	Host    string            `json:"host,omitempty"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// NewInboxTask serializes the payload into a driver.Task ready to pass
// into a HandlerFunc (used in tests).
func NewInboxTask(payload InboxPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeInbox, Body: body}
}

// DecodeInboxPayload extracts an InboxPayload from a task body.
func DecodeInboxPayload(body []byte) (InboxPayload, error) {
	var p InboxPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return InboxPayload{}, err
	}
	return p, nil
}

// DeliverPayload is the body of a deliver task. すべてJSONで安全に
// シリアライズできる型のみを保持する。
type DeliverPayload struct {
	// Inbox is the absolute URL of the recipient inbox to POST to.
	Inbox string `json:"inbox"`
	// Body is the JSON-serialized AP activity to deliver.
	Body []byte `json:"body"`
	// KeyID is the HTTP Signature keyId
	// (e.g. https://example.com/users/u1#main-key).
	KeyID string `json:"keyId"`
	// KeyPEM is the PEM-encoded RSA private key for signing the request.
	KeyPEM string `json:"keyPem"`
	// Ed25519KeyID is the Ed25519 HTTP Signature keyId
	// (e.g. https://example.com/users/u1#ed25519-key) で、recipient が FEP-521a
	// Multikey で Ed25519 を expose していると DeliverService が判断したとき
	// のみ詰める。未設定なら DeliverProcessor は従来通り RSA で sign する
	// (#1067 / #1071)。
	Ed25519KeyID string `json:"ed25519KeyId,omitempty"`
	// Ed25519PrivPEM is the PEM-encoded Ed25519 (PKCS8) private key.
	Ed25519PrivPEM string `json:"ed25519PrivPem,omitempty"`
}

// NewDeliverTask serializes the payload into a driver.Task ready to
// pass into a HandlerFunc (used in tests). DeliverPayload はすべて
// marshal 可能な型 (string と []byte) のみで構成されるため
// json.Marshal は失敗しない。
func NewDeliverTask(payload DeliverPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeDeliver, Body: body}
}

// DecodeDeliverPayload extracts a DeliverPayload from a task body.
func DecodeDeliverPayload(body []byte) (DeliverPayload, error) {
	var p DeliverPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return DeliverPayload{}, err
	}
	return p, nil
}

// ExportPayload is the body of an export task.
type ExportPayload struct {
	UserID string `json:"userId"`
	Type   string `json:"type"` // notes, following, blocking, mute, favorites, user-lists, antennas, clips
}

// NewExportTask creates a driver.Task for data export.
func NewExportTask(payload ExportPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeExport, Body: body}
}

// DecodeExportPayload extracts an ExportPayload from a task body.
func DecodeExportPayload(body []byte) (ExportPayload, error) {
	var p ExportPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return ExportPayload{}, err
	}
	return p, nil
}

// ImportPayload is the body of an import task.
type ImportPayload struct {
	UserID string `json:"userId"`
	Type   string `json:"type"` // following, blocking, muting, user-lists, antennas
	FileID string `json:"fileId"`
}

// NewImportTask creates a driver.Task for data import.
func NewImportTask(payload ImportPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeImport, Body: body}
}

// DecodeImportPayload extracts an ImportPayload from a task body.
func DecodeImportPayload(body []byte) (ImportPayload, error) {
	var p ImportPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return ImportPayload{}, err
	}
	return p, nil
}

// ImportCustomEmojisPayload is the body of an emoji-zip import task.
// UserID は import を発行した管理者 (= drive 上のアップロード所有者)。
type ImportCustomEmojisPayload struct {
	UserID string `json:"userId"`
	FileID string `json:"fileId"`
}

// NewImportCustomEmojisTask creates a driver.Task for emoji-zip imports.
func NewImportCustomEmojisTask(payload ImportCustomEmojisPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeImportCustomEmojis, Body: body}
}

// DecodeImportCustomEmojisPayload extracts an ImportCustomEmojisPayload.
func DecodeImportCustomEmojisPayload(body []byte) (ImportCustomEmojisPayload, error) {
	var p ImportCustomEmojisPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return ImportCustomEmojisPayload{}, err
	}
	return p, nil
}

// WebPushPayload is the body of a Web Push delivery task. The Body
// field holds the already-truncated notification payload; the
// processor does not inspect it and simply forwards it to subscribers.
type WebPushPayload struct {
	UserID string          `json:"userId"`
	Type   string          `json:"type"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// NewWebPushTask serializes the payload into a driver.Task.
func NewWebPushTask(payload WebPushPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeWebPush, Body: body}
}

// DecodeWebPushPayload extracts a WebPushPayload from a task body.
func DecodeWebPushPayload(body []byte) (WebPushPayload, error) {
	var p WebPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return WebPushPayload{}, err
	}
	return p, nil
}

// WebhookPayload carries a single webhook delivery job. Body is the
// pre-marshalled event payload envelope (see core/webhook for the
// exact Misskey-compatible structure); the processor forwards it to
// the endpoint URL without further interpretation.
type WebhookPayload struct {
	WebhookID string          `json:"webhookId"`
	UserID    string          `json:"userId,omitempty"` // user webhooks only
	EventType string          `json:"eventType"`
	Body      json.RawMessage `json:"body"`
}

// NewUserWebhookTask serializes the payload into a driver.Task for
// user webhook delivery.
func NewUserWebhookTask(payload WebhookPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeUserWebhook, Body: body}
}

// NewSystemWebhookTask serializes the payload into a driver.Task for
// system webhook delivery.
func NewSystemWebhookTask(payload WebhookPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeSystemWebhook, Body: body}
}

// DecodeWebhookPayload extracts a WebhookPayload from a task body. The
// same encoder/decoder is reused for both user and system webhook tasks
// since the wire format is identical.
func DecodeWebhookPayload(body []byte) (WebhookPayload, error) {
	var p WebhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return WebhookPayload{}, err
	}
	return p, nil
}

// DeleteAccountPayload carries the userID whose related rows should be
// cascade-deleted by the background processor.
type DeleteAccountPayload struct {
	UserID string `json:"userId"`
}

// NewDeleteAccountTask serializes a DeleteAccountPayload into a driver.Task.
func NewDeleteAccountTask(payload DeleteAccountPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeDeleteAccount, Body: body}
}

// DecodeDeleteAccountPayload extracts a DeleteAccountPayload from a task body.
func DecodeDeleteAccountPayload(body []byte) (DeleteAccountPayload, error) {
	var p DeleteAccountPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return DeleteAccountPayload{}, err
	}
	return p, nil
}

// TaskTypeUnfollow is the task type for the per-pair Unfollow queue
// job. Misskey TS の relationshipQueue の `unfollow` job 名と一致。
// admin/federation/remove-all-following から大量に enqueue されるため
// 個別 row 単位で retry 可能になる (#587)。
const TaskTypeUnfollow = "relationship:unfollow"

// UnfollowPayload identifies the (follower, followee) pair to detach.
// 本家 TS の {from: ThinUser, to: ThinUser, silent: true} に相当。
// silent flag は notification 抑制用だが mk-go の Unfollow は元々
// notification を出さない実装なので payload に含めない。
type UnfollowPayload struct {
	FollowerID string `json:"followerId"`
	FolloweeID string `json:"followeeId"`
}

// NewUnfollowTask serializes an UnfollowPayload into a driver.Task.
func NewUnfollowTask(payload UnfollowPayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypeUnfollow, Body: body}
}

// DecodeUnfollowPayload extracts an UnfollowPayload from a task body.
func DecodeUnfollowPayload(body []byte) (UnfollowPayload, error) {
	var p UnfollowPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return UnfollowPayload{}, err
	}
	return p, nil
}

// PostScheduledNotePayload identifies a draft scheduled for future publish.
// upstream `PostScheduledNoteJobData { noteDraftId }` と shape 一致 (#1040)。
type PostScheduledNotePayload struct {
	NoteDraftID string `json:"noteDraftId"`
}

// NewPostScheduledNoteTask serializes a PostScheduledNotePayload into a
// driver.Task.
func NewPostScheduledNoteTask(payload PostScheduledNotePayload) driver.Task {
	body, _ := json.Marshal(payload)
	return driver.RawTask{TypeName: TaskTypePostScheduledNote, Body: body}
}

// DecodePostScheduledNotePayload extracts a PostScheduledNotePayload from a
// task body.
func DecodePostScheduledNotePayload(body []byte) (PostScheduledNotePayload, error) {
	var p PostScheduledNotePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return PostScheduledNotePayload{}, err
	}
	return p, nil
}
