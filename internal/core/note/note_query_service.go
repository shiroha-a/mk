package note

import (
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// QueryService provides read-only note queries used by notes/* endpoints.
type QueryService struct {
	noteRepo       repository.NoteRepository
	followingRepo  repository.FollowingRepository
	favoriteRepo   repository.NoteFavoriteRepository
	threadMuteRepo repository.NoteThreadMutingRepository
}

// NewQueryService creates a new QueryService.
// followingRepoはfollowers可視性のチェックに使われる。nilを許容する。
func NewQueryService(noteRepo repository.NoteRepository, followingRepo repository.FollowingRepository) *QueryService {
	return &QueryService{noteRepo: noteRepo, followingRepo: followingRepo}
}

// SetFavoriteRepo enables isFavorited reporting on State(). nil でも State
// は動くが isFavorited は常に false になる。
func (s *QueryService) SetFavoriteRepo(r repository.NoteFavoriteRepository) {
	s.favoriteRepo = r
}

// SetThreadMutingRepo enables isMutedThread reporting on State(). nil でも
// State は動くが isMutedThread は常に false になる。
func (s *QueryService) SetThreadMutingRepo(r repository.NoteThreadMutingRepository) {
	s.threadMuteRepo = r
}

// Show returns the requested note if it exists and the viewer can see it.
// viewerはnil可 (未認証ユーザー)。返り値は handler 側で pack されるため、
// Renote / Reply embed まで full preload する (#425 重量経路)。
//
// **visibility check 込み**。federation 経路 (= internal/api/ap で AP
// 経由 lookup する用) では使うが、HTTP API の notes/show は ID 指定なら
// visibility 違反でも note を返す upstream 仕様 (#799) なので、そちらは
// ShowForAPI を使うこと。
func (s *QueryService) Show(viewer *model.User, noteID string) (*model.Note, error) {
	n, err := s.noteRepo.FindByIDWithRelations(noteID)
	if err != nil {
		return nil, ErrNoteNotFound
	}
	if !CanSeeNote(viewer, n, s.followingRepo) {
		return nil, ErrNoteNotFound
	}
	return n, nil
}

// ShowForAPI returns the requested note for the HTTP /api/notes/show
// endpoint without applying the followers-only / specified visibility
// check. upstream Misskey TS が「ID を既に知っている viewer には公開する」
// 設計を採るため (#799) — drop-in 互換のために合わせる。
//
// timeline / replies / renotes / children 等の二次経路は引き続き
// CanSeeNote (= requireVisible) で filter する。federation 経路
// (= ap/handler の Show callsite) も visibility check 維持で leak しない。
func (s *QueryService) ShowForAPI(noteID string) (*model.Note, error) {
	n, err := s.noteRepo.FindByIDWithRelations(noteID)
	if err != nil {
		return nil, ErrNoteNotFound
	}
	return n, nil
}

// RequireVisible loads a single note and verifies the viewer can see it.
// Returns ErrNoteNotFound when the note does not exist OR the viewer cannot
// see it (followers / specified visibility). Used by mutation endpoints that
// must reject non-viewer actions while hiding existence — favorites/create
// (#1443) や clips/add-note (#1456) 等の「ID 既知 viewer が favorite / clip
// など別 channel に note を永続化する」経路で、author が visibility を絞った
// 後も content が漏れ続ける / 存在が漏れる security risk を防ぐためのチェック。
//
// 戻り値の note は handler 側で pack せず捨てるか top-level フィールド
// だけを参照する想定 (#425)。pack 用 full preload が必要なら Show を使う。
func (s *QueryService) RequireVisible(viewer *model.User, noteID string) (*model.Note, error) {
	return s.requireVisible(viewer, noteID)
}

// requireVisible は visibility check 用の軽量ロード。返り値の note は
// 呼び出し元で pack せず捨てるか top-level フィールドだけを参照する想定 (#425)。
// Show は handler に返す note を full preload するため別経路。
func (s *QueryService) requireVisible(viewer *model.User, noteID string) (*model.Note, error) {
	n, err := s.noteRepo.FindByIDWithUser(noteID)
	if err != nil {
		return nil, ErrNoteNotFound
	}
	if !CanSeeNote(viewer, n, s.followingRepo) {
		return nil, ErrNoteNotFound
	}
	return n, nil
}

// CanSee reports whether viewer may see the already-loaded note n, using the
// service's followingRepo for follower checks. requireVisible が「不存在も
// 非閲覧も ErrNoteNotFound に集約して存在隠蔽する」のに対し、caller が
// 「存在は確認済みで可視性だけ別エラーにしたい」場合に使う。例: notes/translate
// は upstream TS に合わせ、非可視を NO_SUCH_NOTE ではなく専用の
// CANNOT_TRANSLATE_INVISIBLE_NOTE で返す (#1445)。
func (s *QueryService) CanSee(viewer *model.User, n *model.Note) bool {
	return CanSeeNote(viewer, n, s.followingRepo)
}

// ListRenotes returns the renotes of the given noteID after filtering for visibility.
// 元のノートが閲覧できない場合はErrNoteNotFoundを返す。
func (s *QueryService) ListRenotes(viewer *model.User, noteID, untilID, sinceID string, limit int) ([]*model.Note, error) {
	if _, err := s.requireVisible(viewer, noteID); err != nil {
		return nil, err
	}
	// visibility は repository が LIMIT 前に SQL push-down する (#1500)。post-fetch
	// filterVisible だとページ過少充填 + followers 判定 N+1 になるため撤去。親 note の
	// requireVisible ゲートは維持。viewer==nil は匿名 (public/home のみ)。
	return s.noteRepo.ListRenotesOf(noteID, viewerIDOf(viewer), untilID, sinceID, limit)
}

// ListReplies returns replies to the given noteID after filtering for visibility.
func (s *QueryService) ListReplies(viewer *model.User, noteID, untilID, sinceID string, limit int) ([]*model.Note, error) {
	if _, err := s.requireVisible(viewer, noteID); err != nil {
		return nil, err
	}
	// visibility は repository が LIMIT 前に SQL push-down する (#1500)。
	return s.noteRepo.ListRepliesOf(noteID, viewerIDOf(viewer), untilID, sinceID, limit)
}

// ListChildren returns notes that reply to or quote the given noteID.
func (s *QueryService) ListChildren(viewer *model.User, noteID, untilID, sinceID string, limit int) ([]*model.Note, error) {
	if _, err := s.requireVisible(viewer, noteID); err != nil {
		return nil, err
	}
	// visibility は repository が LIMIT 前に SQL push-down する (#1500)。
	return s.noteRepo.ListChildrenOf(noteID, viewerIDOf(viewer), untilID, sinceID, limit)
}

// Conversation walks up the reply chain from the given noteID and returns
// up to `limit` ancestors, ordered from oldest to newest. The starting note
// itself is NOT included. Notes the viewer cannot see terminate the walk.
func (s *QueryService) Conversation(viewer *model.User, noteID string, limit, offset int) ([]*model.Note, error) {
	start, err := s.Show(viewer, noteID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}

	var ancestors []*model.Note
	current := start
	// 親をたどる。offset 件分の近い親を skip してから limit 件収集する
	// (upstream conversation.ts: i を increment し i > offset のみ push、
	// conversation.length == limit で停止)。サイクル回避のため訪問済みも記録。
	visited := map[string]struct{}{current.ID: {}}
	i := 0
	for len(ancestors) < limit {
		if current.ReplyID == nil {
			break
		}
		parentID := *current.ReplyID
		if _, dup := visited[parentID]; dup {
			break
		}
		parent, err := s.noteRepo.FindByIDWithRelations(parentID)
		if err != nil {
			break
		}
		if !CanSeeNote(viewer, parent, s.followingRepo) {
			break
		}
		visited[parentID] = struct{}{}
		i++
		// 最初の offset 件 (note に近い親) は skip する。
		if i > offset {
			ancestors = append(ancestors, parent)
		}
		current = parent
	}
	// 古い順 (最深の親を先頭) に並び替える
	reverseNotes(ancestors)
	return ancestors, nil
}

// State returns the note's user-specific state flags.
// 匿名閲覧者 (viewer==nil) はすべて false。
//
// upstream Misskey の notes/state は isFavorited / isMutedThread のみを返す
// (isWatching は廃止) ため、こちらも同じ shape に揃える。
func (s *QueryService) State(viewer *model.User, noteID string) (*NoteState, error) {
	// State は note.ID と note.ThreadID しか触らないので軽量経路で十分 (#425)。
	note, err := s.requireVisible(viewer, noteID)
	if err != nil {
		return nil, err
	}
	state := &NoteState{}
	if viewer == nil {
		return state, nil
	}
	if s.favoriteRepo != nil {
		if ok, err := s.favoriteRepo.Exists(viewer.ID, note.ID); err == nil && ok {
			state.IsFavorited = true
		}
	}
	if s.threadMuteRepo != nil {
		// 本家 Misskey の threadId は note.threadId (ルート) を指す。reply 系の
		// ノートは自身の threadId を、ルートは自身の id を threadId として扱う。
		threadID := note.ID
		if note.ThreadID != nil && *note.ThreadID != "" {
			threadID = *note.ThreadID
		}
		if ok, err := s.threadMuteRepo.Exists(viewer.ID, threadID); err == nil && ok {
			state.IsMutedThread = true
		}
	}
	return state, nil
}

// NoteState represents notes/state response payload.
type NoteState struct {
	IsFavorited   bool `json:"isFavorited"`
	IsMutedThread bool `json:"isMutedThread"`
}

// FilterVisible drops notes the viewer cannot see. Public API for use by
// handlers that perform bulk lookups outside of QueryService (e.g. BulkShow).
func (s *QueryService) FilterVisible(viewer *model.User, rows []*model.Note) []*model.Note {
	return s.filterVisible(viewer, rows)
}

// viewerIDOf returns the viewer's ID, or "" for an anonymous (nil) viewer.
// repository の visibility push-down (applyViewerVisibility) は viewerID="" を
// 匿名 (public/home のみ) として扱う (#1500)。
func viewerIDOf(viewer *model.User) string {
	if viewer == nil {
		return ""
	}
	return viewer.ID
}

// filterVisible drops notes the viewer cannot see.
func (s *QueryService) filterVisible(viewer *model.User, rows []*model.Note) []*model.Note {
	out := make([]*model.Note, 0, len(rows))
	for _, n := range rows {
		if CanSeeNote(viewer, n, s.followingRepo) {
			out = append(out, n)
		}
	}
	return out
}

func reverseNotes(notes []*model.Note) {
	for i, j := 0, len(notes)-1; i < j; i, j = i+1, j-1 {
		notes[i], notes[j] = notes[j], notes[i]
	}
}
