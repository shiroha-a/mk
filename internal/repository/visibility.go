package repository

import "gorm.io/gorm"

// applyViewerVisibility appends the core/note.CanSeeNote-equivalent visibility
// predicate to a query over the note table (unqualified columns). Empty
// viewerID means an anonymous viewer, which can only see public/home notes.
//
// visibility IDOR push-down (#1418 / #1439 / #1441 / #1486 / #1487 等) で各
// repository メソッドにコピペされていた同一述語を 1 箇所に集約する (#1454)。
// instance-block / muted-user 等の条件を将来足すときも、ここ 1 箇所を直せば
// 全 push-down 経路に反映されるので drift (条件の食い違い) を防げる。
//
// 注意: list timeline 系 (ListByUserList) は DM 非表示のため specified を含めない
// 別述語を使う。本 helper は specified を含む完全な CanSeeNote 版なので、そちらに
// は使わない。
func applyViewerVisibility(q *gorm.DB, viewerID string) *gorm.DB {
	if viewerID == "" {
		return q.Where(`"visibility" IN ('public','home')`)
	}
	// #2106 N27: upstream QueryService.generateVisibilityQuery 相当の OR を網羅する。
	// visibleUserIds / mentions は visibility 横断で許可し (specified に限らない)、
	// followers 分岐には reply 先が viewer のケース (replyUserId=viewer) を足す。これが
	// 無いと followers note の mention/reply/visibleUser 宛が read 経路で過剰に隠れる。
	// (main-stream realtime push の #1472 strict gate は core/note.canSeeNoteForStream
	// 側で別途維持しており、本 helper の read 緩和とは独立。)
	return q.Where(
		`("visibility" IN ('public','home') `+
			`OR "userId" = ? `+
			`OR ? = ANY("visibleUserIds") `+
			`OR ? = ANY("mentions") `+
			`OR ("visibility" = 'followers' AND ("userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?) OR "replyUserId" = ?)))`,
		viewerID, viewerID, viewerID, viewerID, viewerID)
}

// applyViewerVisibilityExists is the correlated-EXISTS variant of
// applyViewerVisibility for tables that list rows referencing notes of mixed
// authors (clip_note / note_reaction). It filters by a CanSeeNote-equivalent
// predicate against the note table aliased `v`, joined on noteIDColumn (e.g.
// `"clip_note"."noteId"`). Empty viewerID = anonymous (public/home only).
//
// noteIDColumn は内部の固定識別子のみを渡すこと (呼び出し側のリテラル)。ユーザー
// 入力を渡さない前提なので文字列連結は injection-safe。viewerID は bind param。
func applyViewerVisibilityExists(q *gorm.DB, noteIDColumn, viewerID string) *gorm.DB {
	if viewerID == "" {
		return q.Where(`EXISTS (SELECT 1 FROM "note" v WHERE v."id" = ` + noteIDColumn + ` AND v."visibility" IN ('public','home'))`)
	}
	// #2106 N27: applyViewerVisibility と同じく generateVisibilityQuery 相当に揃える。
	return q.Where(
		`EXISTS (SELECT 1 FROM "note" v WHERE v."id" = `+noteIDColumn+` AND (`+
			`v."visibility" IN ('public','home') `+
			`OR v."userId" = ? `+
			`OR ? = ANY(v."visibleUserIds") `+
			`OR ? = ANY(v."mentions") `+
			`OR (v."visibility" = 'followers' AND (v."userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?) OR v."replyUserId" = ?))))`,
		viewerID, viewerID, viewerID, viewerID, viewerID)
}
