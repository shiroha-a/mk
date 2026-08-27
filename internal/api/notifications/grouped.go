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
// 本家 Misskey TS i/notifications-grouped.ts:107-155 の grouping ロジック準拠:
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

	// 各通知を単体 pack して packed map を得る。packed の各要素には note / user /
	// reaction が既に埋まっている (#1444 の可視性ゲート適用済み) ので、grouping では
	// それらを再利用するだけで良い。
	//
	// **packed は filtered と 1:1 とは限らない。** PackNotifications は roleAssigned
	// (role 削除済) / chatRoomInvitationReceived (invitation 削除済) を通知ごと drop
	// する。突き合わせは groupNotifications 側で id を使って行う (#2736)。
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
	// 本家 i/notifications-grouped.ts:153 と同じく grouping 後に limit で slice する。
	// **現状は到達しない** — collectNotifications が svc.List に limit を渡して既に
	// 切っており、grouping は件数を増やさないため。upstream と同じ位置に置くことで、
	// 将来 fetch 側が多めに取るようになっても upstream と同じ行が返るようにしてある。
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
// 最後に畳んだ通知の id を採用する。一覧は既定で降順なので通常は「最も古い」だが、
// sinceId 単独のページは昇順になる (Service.List の ascending) ので断定はできない。
//
// filtered と packed はどちらも collectNotifications の出力順だが **長さは一致する
// とは限らない**。noteByID は renote notification の target note 判定 (RenoteID) に使う。
func groupNotifications(filtered []*notification.Notification, packed []map[string]any, noteByID map[string]*model.Note) []map[string]any {
	// packed は pack 時に drop された通知の分だけ短い (roleAssigned で role 削除済 /
	// chatRoomInvitationReceived で invitation 削除済)。filtered の index で packed を
	// 引くと drop が 1 件でもあれば範囲外に出て panic → 500 になっていた (#2736)。
	// id で引き直す。
	//
	// **pack 段の drop は「行は出さないが区切りとしては残る」。** upstream は raw 列で
	// grouping してから packGroupedMany → null を落とす順なので、drop を挟んだ 2 件は
	// 畳まれない。drop 済みを列から抜いてから grouping すると跨いで畳んでしまう。
	//
	// **ただし collectNotifications 段の drop は区切りにならない。** upstream は
	// notifier 不在 / mute / suspended、削除済 note、解決済み receiveFollowRequest も
	// grouping の**後**に落とすが、mk-go はこれらを grouping より前に filtered から
	// 抜いている。そこは upstream と挙動が違う (本 fix 以前からの乖離、#2739)。
	//
	// **不可視 note はさらに種類が違う。** upstream は hideNote で blank するだけで
	// 行は残すが、mk-go は行ごと落とす (#1444 / #1953)。順序ではなく drop か blank かの
	// 違いなので、走査位置を揃えても一致しない。
	out := make([]map[string]any, 0, len(packed))
	byID := make(map[string]map[string]any, len(packed))
	for _, p := range packed {
		byID[asString(p["id"])] = p
	}
	for i, cur := range filtered {
		curPacked := byID[cur.ID]
		if curPacked == nil {
			// pack で drop された通知。行は出さないが、次の周回で prev として
			// grouping 条件に評価されることで区切りとして働く。
			continue
		}
		// len(out) > 0 も prevPacked != nil と同様、外しても落ちるテストは無い
		// (先頭行が drop される fixture が無い)。両方外すと out[-1] で落ちる。
		if i > 0 && len(out) > 0 {
			prev := filtered[i-1]
			// prevPacked が nil になるのは prev が drop 済みのとき = grouping 対象外の
			// 型のときだけなので、この判定は現状の結果を変えない。防御として置く
			// (詳細は groupInto の doc)。
			if prevPacked := byID[prev.ID]; prevPacked != nil &&
				groupInto(out, prev, prevPacked, cur, curPacked, noteByID) {
				continue
			}
		}
		out = append(out, curPacked)
	}
	return out
}

// groupInto folds `cur` into the last output row when it continues a reaction /
// renote group seeded by `prev`. Returns true when folded, in which case the
// caller must not append `cur` as its own row.
//
// out[len(out)-1] が prev の行 (または prev を含むグループ) であることを前提にする。
// prev が pack されていれば直前の周回で append か fold されているので成立する。
//
// 呼び出し元の `prevPacked != nil` 判定は**到達するが結果を変えない** — prev が
// drop 済みなのは roleAssigned / chatRoomInvitationReceived のときだけで、その 2 型は
// grouping 条件のどちらにも当たらないため。外しても落ちるテストは無いが、grouping
// 対象の型が増えるか、reaction / renote を drop する条件が入ると前提を保つ唯一の
// 砦になるので残す。
func groupInto(out []map[string]any, prev *notification.Notification, prevPacked map[string]any, cur *notification.Notification, curPacked map[string]any, noteByID map[string]*model.Note) bool {
	last := out[len(out)-1]

	// reaction grouping: prev/cur 両方 reaction かつ同一 NoteID。
	if prev.Type == notification.TypeReaction && cur.Type == notification.TypeReaction &&
		prev.NoteID != "" && prev.NoteID == cur.NoteID {
		if asString(last["type"]) != "reaction:grouped" {
			last = newReactionGroup(prev, prevPacked)
			out[len(out)-1] = last
		}
		appendReaction(last, cur, curPacked)
		last["id"] = cur.ID
		return true
	}

	// renote grouping: prev/cur 両方 renote かつ同一 target note。
	// mk-go の renote 通知は NoteID に renote note 自体を持ち、target note は
	// その RenoteID。noteByID から各 renote note を引いて RenoteID を比較する。
	if prev.Type == notification.TypeRenote && cur.Type == notification.TypeRenote {
		if pt, ct := renoteTargetID(prev, noteByID), renoteTargetID(cur, noteByID); pt != "" && pt == ct {
			if asString(last["type"]) != "renote:grouped" {
				last = newRenoteGroup(prev, prevPacked)
				// #2106 L23: upstream i/notifications-grouped は renote group seed の createdAt に
				// cur (= 一覧の並びで 1 つ後。降順ページなら古い方) 通知の時刻を採る
				// (reaction group は prev で一致)。newRenoteGroup は prev の createdAt を
				// 入れるので cur で上書きする。
				last["createdAt"] = curPacked["createdAt"]
				out[len(out)-1] = last
			}
			appendRenoteUser(last, curPacked)
			last["id"] = cur.ID
			return true
		}
	}
	return false
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
