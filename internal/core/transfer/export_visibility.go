package transfer

import (
	"fmt"
	"time"

	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
)

// exportableFor reports whether a note referenced by the exporting user's
// favorites / clips may be written to the export file.
//
// upstream ExportFavoritesProcessorService / ExportClipsProcessorService は
// generateVisibilityQuery(query, { id: user.id }) で行を絞り、さらに
// shouldHideNoteByTime(note.user.makeNotesHiddenBefore) に当たる行を飛ばす。
// favorite / clip は「保存した時点で見えた」ノートの ID を持ち続けるので、後で
// フォローを外した (外された) followers ノートや、自分が宛先から外れた specified
// ノートがそのまま残る。これを絞らないと i/favorites の hide を export で迂回
// して本文・添付 ID・宛先一覧を読めてしまう。
//
// 見えないものは hide ではなく行ごと落とす (upstream と同じ)。
func (e *Exporter) exportableFor(viewer *model.User, n *model.Note) (bool, error) {
	if !corenote.CanSeeNote(viewer, n, e.deps.FollowingRepo) {
		return false, nil
	}
	author := n.User
	if author == nil {
		u, err := e.deps.UserRepo.FindByID(n.UserID)
		if err != nil {
			return false, fmt.Errorf("find note author %s: %w", n.UserID, err)
		}
		author = u
	}
	if author == nil || author.MakeNotesHiddenBefore == nil {
		return true, nil
	}
	// 作成時刻が分からないときは「古い」側に倒す (epoch 0 扱い = 隠す)。
	// 期間設定をした作者のノートを判定不能のまま出すと、設定が効かなくなる。
	var createdAtMs int64
	if e.deps.IDGen != nil {
		if t, err := e.deps.IDGen.ParseTime(n.ID); err == nil {
			createdAtMs = t.UnixMilli()
		}
	}
	return !corenote.ShouldHideNoteByTime(author.MakeNotesHiddenBefore, createdAtMs, time.Now().UnixMilli()), nil
}
