package notifications

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// Grouped handles POST /api/i/notifications-grouped.
//
// 本家 Misskey TS i/notifications-grouped.ts:108-160 の grouping ロジック準拠:
// 連続する同一 note への reaction を `reaction:grouped` (reactions[] 集約)、
// 連続する同一 target note への renote を `renote:grouped` (users[] 集約) に
// 畳み込んでから packGroupedMany 相当の shape で返す。それ以外の通知は
// i/notifications と同じ単体 shape のまま残す。
//
// fetch / cursor / type filter / CanSeeNote ゲート (#1444) は Show と共通の
// collectNotifications を使うので、grouped 側でも note embed の可視性は保たれる。
func (h *Handler) Grouped(c echo.Context) error {
	user := middleware.GetUser(c)
	req, ok := h.bindListRequest(c)
	if !ok {
		return apierr.JSONInvalidParam(c)
	}

	filtered, notifierByID, noteByID, err := h.collectNotifications(c, user, req)
	if err != nil {
		return apierr.JSONInternalError(c)
	}

	// 各通知を単体 pack して packed map を得る。PackNotifications は N==nil を
	// skip するが filtered は全件 non-nil なので index は filtered と 1:1 で
	// 揃う。packed[i] には note / user / reaction が既に埋まっている (#1444 の
	// 可視性ゲート適用済み) ので、grouping ではそれらを再利用するだけで良い。
	items := make([]entity.NotificationItem, 0, len(filtered))
	for _, n := range filtered {
		items = append(items, entity.NotificationItem{
			N:    n,
			User: notifierByID[n.NotifierID],
			Note: noteByID[n.NoteID],
		})
	}
	packed := entity.PackNotifications(items, h.idGen, h.instanceLookup(), h.emojiLookup(), h.notificationOptions(user.ID)...)
	// depth-2 embed hide (#1570): grouping の前に通知 note の renote/reply embed と
	// 著者設定ゲートを viewer 可視性で適用する。Show と同じく #1444 CanSeeNote gate は
	// 落とすだけで embed に再帰しないため、ここで hide しないと grouped 経由で
	// 非可視 embed 本文が漏れる (#1546 で混入しかけた IDOR を塞ぐ)。
	notehide.HideNotificationNotes(user, packed)

	grouped := groupNotifications(filtered, packed, noteByID)
	// 本家 update.ts と同じく grouping 後に limit で slice する。
	if len(grouped) > (*req.Limit) {
		grouped = grouped[:(*req.Limit)]
	}

	h.maybeMarkAsRead(c, user, req)
	return c.JSON(http.StatusOK, grouped)
}

// groupNotifications folds consecutive reaction / renote notifications into the
// grouped shape, mirroring upstream Misskey TS i/notifications-grouped.ts.
//
//   - 連続する reaction で同一 note (= 同 NoteID) なら `reaction:grouped` に
//     畳み込み、reactions[] に {user, reaction} を push する。
//   - 連続する renote で同一 target note (= renote note の RenoteID) なら
//     `renote:grouped` に畳み込み、users[] に notifier user を push する。
//
// それ以外は単体 packed map をそのまま残す。グループの id は本家同様グループ内
// 最後 (= 最も古い、newest-first の末尾) の通知 id を採用する。
//
// filtered と packed は同じ index 順 (collectNotifications の出力順 = newest
// first)。noteByID は renote notification の target note 判定 (RenoteID) に使う。
func groupNotifications(filtered []*notification.Notification, packed []map[string]any, noteByID map[string]*model.Note) []map[string]any {
	out := make([]map[string]any, 0, len(packed))
	if len(packed) == 0 {
		return out
	}
	out = append(out, packed[0])
	for i := 1; i < len(filtered); i++ {
		cur := filtered[i]
		prev := filtered[i-1]
		last := out[len(out)-1]

		// reaction grouping: prev/cur 両方 reaction かつ同一 NoteID。
		if prev.Type == notification.TypeReaction && cur.Type == notification.TypeReaction &&
			prev.NoteID != "" && prev.NoteID == cur.NoteID {
			if asString(last["type"]) != "reaction:grouped" {
				last = newReactionGroup(prev, packed[i-1])
				out[len(out)-1] = last
			}
			appendReaction(last, cur, packed[i])
			last["id"] = cur.ID
			continue
		}

		// renote grouping: prev/cur 両方 renote かつ同一 target note。
		// mk-go の renote 通知は NoteID に renote note 自体を持ち、target note は
		// その RenoteID。noteByID から各 renote note を引いて RenoteID を比較する。
		if prev.Type == notification.TypeRenote && cur.Type == notification.TypeRenote {
			if pt, ct := renoteTargetID(prev, noteByID), renoteTargetID(cur, noteByID); pt != "" && pt == ct {
				if asString(last["type"]) != "renote:grouped" {
					last = newRenoteGroup(prev, packed[i-1])
					// #2106 L23: upstream i/notifications-grouped は renote group seed の createdAt に
					// cur (= group 内 2 番目 = より古い) 通知の時刻を採る (reaction group は prev で一致)。
					// newRenoteGroup は prev の createdAt を入れるので cur で上書きする。
					last["createdAt"] = packed[i]["createdAt"]
					out[len(out)-1] = last
				}
				appendRenoteUser(last, packed[i])
				last["id"] = cur.ID
				continue
			}
		}

		out = append(out, packed[i])
	}
	return out
}

// renoteTargetID returns the target note id of a renote notification (= the
// RenoteID of the renote note stored under the notification's NoteID). Returns
// "" when the renote note is not visible / fetchable so non-resolvable renotes
// never group (fail-closed: distinct rows stay separate rather than collapsing
// on an empty key).
func renoteTargetID(n *notification.Notification, noteByID map[string]*model.Note) string {
	if n.NoteID == "" {
		return ""
	}
	note := noteByID[n.NoteID]
	if note == nil || note.RenoteID == nil {
		return ""
	}
	return *note.RenoteID
}

// newReactionGroup seeds a reaction:grouped entry from the first packed
// reaction notification. note は packed map から引き継ぐ (可視性ゲート済み)。
func newReactionGroup(first *notification.Notification, firstPacked map[string]any) map[string]any {
	g := map[string]any{
		"id":        first.ID,
		"createdAt": firstPacked["createdAt"],
		"type":      "reaction:grouped",
		"reactions": []map[string]any{reactionEntry(firstPacked)},
	}
	if note, ok := firstPacked["note"]; ok {
		g["note"] = note
	}
	return g
}

// appendReaction pushes one {user, reaction} entry onto a reaction:grouped.
func appendReaction(group map[string]any, _ *notification.Notification, packed map[string]any) {
	arr, _ := group["reactions"].([]map[string]any)
	group["reactions"] = append(arr, reactionEntry(packed))
}

// reactionEntry builds the {user, reaction} pair from a single packed reaction
// notification map.
func reactionEntry(packed map[string]any) map[string]any {
	e := map[string]any{}
	if u, ok := packed["user"]; ok {
		e["user"] = u
	}
	if r, ok := packed["reaction"]; ok {
		e["reaction"] = r
	}
	return e
}

// newRenoteGroup seeds a renote:grouped entry from the first packed renote
// notification. users[] holds packed notifier users; note は packed map から
// 引き継ぐ。
func newRenoteGroup(first *notification.Notification, firstPacked map[string]any) map[string]any {
	g := map[string]any{
		"id":        first.ID,
		"createdAt": firstPacked["createdAt"],
		"type":      "renote:grouped",
		"users":     renoteUsers(firstPacked),
	}
	if note, ok := firstPacked["note"]; ok {
		g["note"] = note
	}
	return g
}

// appendRenoteUser pushes one packed notifier user onto a renote:grouped.
func appendRenoteUser(group map[string]any, packed map[string]any) {
	arr, _ := group["users"].([]any)
	if u, ok := packed["user"]; ok {
		arr = append(arr, u)
	}
	group["users"] = arr
}

// renoteUsers builds the initial users[] slice for a renote:grouped from the
// first packed notification's user (may be empty when the notifier user could
// not be resolved).
func renoteUsers(packed map[string]any) []any {
	if u, ok := packed["user"]; ok {
		return []any{u}
	}
	return []any{}
}

// asString returns the string value of v or "" when v is not a string.
func asString(v any) string {
	s, _ := v.(string)
	return s
}
