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
	return q.Where(
		`("visibility" IN ('public','home') `+
			`OR "userId" = ? `+
			`OR ("visibility" = 'followers' AND "userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
			`OR ("visibility" = 'specified' AND ? = ANY("visibleUserIds")))`,
		viewerID, viewerID, viewerID)
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
	return q.Where(
		`EXISTS (SELECT 1 FROM "note" v WHERE v."id" = `+noteIDColumn+` AND (`+
			`v."visibility" IN ('public','home') `+
			`OR v."userId" = ? `+
			`OR (v."visibility" = 'followers' AND v."userId" IN (SELECT f."followeeId" FROM "following" f WHERE f."followerId" = ?)) `+
			`OR (v."visibility" = 'specified' AND ? = ANY(v."visibleUserIds"))))`,
		viewerID, viewerID, viewerID)
}
