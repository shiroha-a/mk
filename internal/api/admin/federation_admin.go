package admin

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
)

// FederationDeleteAllFiles handles POST /api/admin/federation/delete-all-files.
//
// 指定ホストの DriveFile (= リモート user の上げた添付) を一括削除する。
// 旧実装は no-op だったが #587 で本実装化。Misskey TS 本家の挙動と等価:
// packages/backend/src/server/api/endpoints/admin/federation/delete-all-files.ts。
//
// host が空または driveFileRepo 未配線の場合は no-op で 204 を返す。S3 等の
// 物理オブジェクト削除は drive_file の deletion hook 経由 (別経路、本家と同じく
// row 削除で hook が起動する)。
func (h *Handler) FederationDeleteAllFiles(c echo.Context) error {
	var req struct {
		Host string `json:"host"`
	}
	_ = c.Bind(&req)
	if req.Host == "" || h.driveFileRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	if _, err := h.driveFileRepo.DeleteByHost(req.Host); err != nil {
		return apierr.JSONInternalError(c)
	}
	return c.NoContent(http.StatusNoContent)
}

// FederationRefreshRemoteInstanceMetadata handles POST /api/admin/federation/refresh-remote-instance-metadata.
// InstanceMetadataFetcher (= coreinstance.FetchMetadataService) 経由で指定ホストの
// nodeinfo + iconUrl / faviconUrl を再取得する。fetcher 未設定または host 未指定
// の場合は no-op で 204 を返す (本家 TS も失敗時エラーコードは返さない挙動)。
func (h *Handler) FederationRefreshRemoteInstanceMetadata(c echo.Context) error {
	var req struct {
		Host string `json:"host"`
	}
	_ = c.Bind(&req)
	if h.instanceMetadataFetcher == nil || req.Host == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// fetch 失敗はユーザーへ明示的にエラー返す必要はない (frontendは成功前提
	// でUI更新するだけ)。ログに残してリトライ可能な状態にしておく。
	if err := h.instanceMetadataFetcher.Fetch(req.Host); err != nil {
		slog.Warn("federation refresh metadata failed", "host", req.Host, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// FederationRemoveAllFollowing handles POST /api/admin/federation/remove-all-following.
//
// 指定ホストの Follower (= 当インスタンスのローカル user を follow している
// リモート user) を全員 unfollow させる。followerHost = req.Host の Following
// 行を列挙し、各 (followerID, followeeID) ペアごとに per-pair Unfollow ジョブを
// キューに enqueue する。実際の row 削除と Reject(Follow) 配送は worker
// (processors.UnfollowProcessor) が core/following.Service.Unfollow を呼んで
// 行うので、HTTP request はブロックされない。
//
// 旧実装は no-op だったが #587 で本実装化。Misskey TS 本家
// (packages/backend/src/server/api/endpoints/admin/federation/remove-all-following.ts
// が queueService.createUnfollowJob 経由で per-pair queueing する) と挙動を
// 揃える。host 未指定 / 依存未配線の場合は no-op で 204 を返す。
//
// Worker 側で処理されるため、enumeration 中に row 削除は発生しない。よって
// offset ベースの pagination で安全に列挙できる (sync 版で必要だった seen-set
// は不要)。
func (h *Handler) FederationRemoveAllFollowing(c echo.Context) error {
	var req struct {
		Host string `json:"host"`
	}
	_ = c.Bind(&req)
	if req.Host == "" || h.followingRepo == nil || h.unfollowEnqueuer == nil {
		return c.NoContent(http.StatusNoContent)
	}
	const pageSize = 100
	const maxBatches = 1000 // safety cap: 100k pairs / request
	for batch := 0; batch < maxBatches; batch++ {
		rows, err := h.followingRepo.ListFollowersByHost(req.Host, pageSize, batch*pageSize)
		if err != nil {
			return apierr.JSONInternalError(c)
		}
		if len(rows) == 0 {
			break
		}
		for _, f := range rows {
			if err := h.unfollowEnqueuer.EnqueueUnfollow(queue.UnfollowPayload{
				FollowerID: f.FollowerID,
				FolloweeID: f.FolloweeID,
			}); err != nil {
				// enqueue 失敗は個別ペアで握りつぶす - admin 操作の best-effort
				// として残りの enqueue を妨げない。Worker retry が効かないため
				// ログには残しておく。
				slog.Warn("admin: federation/remove-all-following: enqueue failed",
					"host", req.Host,
					"follower", f.FollowerID,
					"followee", f.FolloweeID,
					"err", err)
			}
		}
		if len(rows) < pageSize {
			break
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// federationUpdateInstanceRequest is the JSON shape for
// /api/admin/federation/update-instance. ModerationNote は **pointer** にして
// 「未送信 (nil)」と「明示的に空文字列で clear」を区別する (#675)。
// json.Unmarshal は欠落 field を nil pointer のまま残すので、両者を JSON
// decode 境界で正しく分離できる。string 型だと "" がデフォルト値と区別でき
// ず空文字列で note を消す操作が無視される (元バグ)。
type federationUpdateInstanceRequest struct {
	Host           string  `json:"host"`
	IsSuspended    *bool   `json:"isSuspended"`
	IsBlocked      *bool   `json:"isBlocked"`
	IsSilenced     *bool   `json:"isSilenced"`
	ModerationNote *string `json:"moderationNote"`
}

// updates derives the GORM Updates(map) payload from the request. Only
// fields explicitly sent in the request are included so the caller can
// "clear" string fields by sending "" (and not "leave unchanged" via
// omitting the field).
func (req federationUpdateInstanceRequest) updates() map[string]any {
	out := map[string]any{}
	if req.IsSuspended != nil {
		out["isSuspended"] = *req.IsSuspended
	}
	if req.IsBlocked != nil {
		out["isBlocked"] = *req.IsBlocked
	}
	if req.IsSilenced != nil {
		out["isSilenced"] = *req.IsSilenced
	}
	if req.ModerationNote != nil {
		// pointer != nil なら明示的に送信されたので空文字列でも反映する。
		out["moderationNote"] = *req.ModerationNote
	}
	return out
}

// FederationUpdateInstance handles POST /api/admin/federation/update-instance.
//
// Misskey TS 互換 (admin/federation/update-instance.ts) で `isSuspended` 変更時に
// `suspendRemoteInstance` / `unsuspendRemoteInstance`、`moderationNote` 変更時に
// `updateRemoteInstanceNote` の moderation_log を **個別に** 出力する。1 リクエ
// ストで両方変更されれば 2 ログ。`isBlocked` / `isSilenced` は TS 仕様に該当
// type が無いので skip。
//
// instance 行の lookup / 更新は `InstanceRepository` (#676 で DI 化) を経由する。
// 未配線時は no-op で 204 を返す (元の挙動と一貫)。
func (h *Handler) FederationUpdateInstance(c echo.Context) error {
	if h.instanceRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req federationUpdateInstanceRequest
	if err := c.Bind(&req); err != nil || req.Host == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// before snapshot for moderation log diff (`isSuspended` の変化判定 +
	// `moderationNote` の before/after)。lookup 失敗は instance 不在として
	// no-op で 204 を返す (元の挙動は単純 Updates 1 回だけだったので失敗時
	// silent と一貫)。
	beforePtr, err := h.instanceRepo.FindByHost(req.Host)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	// 値コピーで snapshot を凍結する: UpdateFields の実装によっては同じ struct
	// を mutate することがあり (例: in-memory mock)、後段の moderation log diff
	// で before/after が両方 after 値になる退行を防ぐ。
	before := *beforePtr
	if updates := req.updates(); len(updates) > 0 {
		_ = h.instanceRepo.UpdateFields(req.Host, updates)
	}
	// suspend / unsuspend と moderationNote 変更で個別 log を出す。
	// SuspensionState != "none" を「suspended」と扱う。
	beforeSuspended := before.SuspensionState != model.SuspensionStateNone
	if req.IsSuspended != nil && *req.IsSuspended != beforeSuspended {
		t := moderationlog.LogSuspendRemoteInstance
		if !*req.IsSuspended {
			t = moderationlog.LogUnsuspendRemoteInstance
		}
		h.logModeration(c, t, map[string]any{
			"id":   before.ID,
			"host": before.Host,
		})
	}
	if req.ModerationNote != nil && *req.ModerationNote != before.ModerationNote {
		h.logModeration(c, moderationlog.LogUpdateRemoteInstanceNote, map[string]any{
			"id":     before.ID,
			"host":   before.Host,
			"before": before.ModerationNote,
			"after":  *req.ModerationNote,
		})
	}
	return c.NoContent(http.StatusNoContent)
}
