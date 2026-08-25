package timeline

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// MaxTimelineLength caps the number of IDs kept in each Redis timeline list.
// Misskey本家のデフォルトと同じ200。
const MaxTimelineLength = 200

// Errors returned by Service.
var (
	// ErrUnauthenticated is returned by Home/Hybrid timelines when no user is provided.
	ErrUnauthenticated = errors.New("user is required for this timeline")
)

// NoteSource is the minimum interface required by Service to resolve note IDs
// and to fall back to a database scan when Redis is empty.
type NoteSource interface {
	FindManyByIDsWithUser(ids []string) ([]*model.Note, error)
}

// Service exposes the four timeline endpoints (home/local/global/hybrid).
// Reads always go through Redis first; on a miss it falls back to a direct
// repository query.
type Service struct {
	fanout        *FanoutTimelineService
	noteRepo      repository.NoteRepository
	followingRepo repository.FollowingRepository
	// ephemeral はリレー経由でしか観測しない投稿の置き場 (#2332)。DB に無い
	// ID をここから補う。nil なら従来どおり DB のみ。
	ephemeral EphemeralNoteLookup
	// fanoutToggle は meta.enableFanoutTimeline。nil なら常に有効扱い。
	fanoutToggle FanoutToggleProvider
}

// EphemeralNoteLookup resolves notes that live only in Redis (#2332).
// Implemented by ephemeral.Store; ここで narrow interface にしておくことで
// timeline のテストが Redis 無しで書ける。
type EphemeralNoteLookup interface {
	GetNotes(ctx context.Context, ids []string) ([]*model.Note, error)
}

// NewService creates a new timeline Service.
func NewService(fanout *FanoutTimelineService, noteRepo repository.NoteRepository, followingRepo repository.FollowingRepository) *Service {
	return &Service{fanout: fanout, noteRepo: noteRepo, followingRepo: followingRepo}
}

// SetEphemeralLookup attaches the ephemeral note store so timelines can show
// relay-delivered notes that were never written to the database (#2332).
// Optional — nil keeps the database-only behaviour.
func (s *Service) SetEphemeralLookup(l EphemeralNoteLookup) {
	s.ephemeral = l
}

// SetFanoutToggle attaches meta.enableFanoutTimeline so that reads bypass Redis
// entirely while FTT is off (upstream timeline endpoint の
// `if (!serverSettings.enableFanoutTimeline) return getFromDb()` 相当)。
//
// gate が無いと、FTT を切った後も Redis に残った ID が読まれ続ける。push 側も
// 止まっているので古い ID が押し出されることも無く、タイムラインが過去の内容で
// 固まってしまう。
func (s *Service) SetFanoutToggle(p FanoutToggleProvider) {
	s.fanoutToggle = p
}

// fanoutEnabled reports whether FTT is on. Provider 未配線なら有効扱い。
func (s *Service) fanoutEnabled() bool {
	if s.fanoutToggle == nil {
		return true
	}
	return s.fanoutToggle.FanoutTimelineEnabled()
}

// fallbackRange narrows the database fallback to the range *beyond* what was
// already **resolved**, so the fan-out result is topped up instead of being
// thrown away.
//
// upstream FanoutTimelineEndpointService は Redis から取れた分を残したまま、
// 最後に読んだ ID を境界にして「足りない分だけ」を DB から継ぎ足す
// (`dbUntil = noteIds[noteIds.length - 1]` → `[...redisTimeline, ...gotFromDb]`)。
// mk-go は以前、件数が足りないと同じ範囲を DB で引き直して Redis 側の結果ごと
// 置き換えていたため、fanout が配った note (per-follow withReplies を反映した
// 返信など) が DB query の条件で消える現象が起きていた。
//
// sinceID のみ指定された昇順ページングでは境界が逆になるので、返す since/until
// を入れ替える。
//
// **境界は「Redis から取れた ID」ではなく「実際に解決できた note」から取る**
// (#2715)。ephemeral の TTL が切れると note の実体だけが消えて ID が list に残る。
// その ID を境界にすると、**解決できない古い ID より新しい投稿が DB にあっても
// 返らなくなる** — 実測で home timeline の ID の約半分が解決できない状態になって
// おり、リレー由来でない投稿まで出てこなくなっていた。解決 0 件なら境界を使わず、
// 呼び出し側の since/until をそのまま渡す (= 最新から返す)。
func fallbackRange(notes []*model.Note, sinceID, untilID string) (fbSince, fbUntil string) {
	if len(notes) == 0 {
		return sinceID, untilID
	}
	boundary := notes[len(notes)-1].ID
	if sinceID != "" && untilID == "" {
		return boundary, untilID
	}
	return sinceID, boundary
}

// HomeTimeline returns the timeline for a logged-in user. The home timeline
// shows notes by users they follow plus their own notes.
func (s *Service) HomeTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if viewer == nil {
		return nil, ErrUnauthenticated
	}
	if limit <= 0 {
		limit = 20
	}
	// upstream notes/timeline の getFromDb は withReplies を持たず、常に
	// 「返信ではない or 自己スレッド」だけを返す。返信を出すかどうかは
	// `following.withReplies` を見る fanout (push) 側の責務。
	dbFilter := toDBFilter(filter, viewer.ID)
	dbFilter.ExcludeRepliesToOthers = true
	if !s.fanoutEnabled() {
		return s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, dbFilter)
	}
	ids, err := s.fanout.Get(ctx, HomeTimelineName(viewer.ID), untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		resolved, dangling, err := s.resolve(ctx, ids)
		if err != nil {
			return nil, err
		}
		s.pruneDangling(ctx, []Name{HomeTimelineName(viewer.ID)}, dangling)
		notes := ApplyFilter(resolved, viewer.ID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			// **境界は filter 前の resolved から取る。** filter で落ちた note も
			// 「解決はできている」ので、DB fallback で引き直す必要は無い。
			fbSince, fbUntil := fallbackRange(resolved, sinceID, untilID)
			rest, err := s.noteRepo.ListHomeTimeline(viewer.ID, limit-len(notes), fbSince, fbUntil, dbFilter)
			if err != nil {
				return nil, err
			}
			return append(notes, rest...), nil
		}
		return notes, nil
	}
	return s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, dbFilter)
}

// LocalTimeline returns notes posted by local users with public/home visibility.
func (s *Service) LocalTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 20
	}
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	// upstream は LTL を返信の有無で 3 本に分けて持ち、取得時に合流させる。
	//   withReplies=true  -> localTimeline + localTimelineWithReplies
	//   viewer あり        -> localTimeline + localTimelineWithReplyTo:<viewer>
	//   viewer なし        -> localTimeline のみ
	// これで「他人の他人への返信は出ない」「自分宛ての返信は出る」が両立する。
	// upstream local-timeline の getFromDb は withReplies パラメータ (既定
	// false) が偽なら「返信ではない or 自己スレッド」に絞る。mk-go は未指定
	// (nil) を false と同じ扱いにする必要がある。
	dbFilter := toDBFilter(filter, viewerID)
	if filter.WithReplies == nil || !*filter.WithReplies {
		dbFilter.ExcludeRepliesToOthers = true
	}
	if !s.fanoutEnabled() {
		return s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, dbFilter)
	}
	keys := []Name{LocalTimeline}
	switch {
	case filter.WithReplies != nil && *filter.WithReplies:
		keys = append(keys, LocalTimelineWithReplies)
	case viewerID != "":
		keys = append(keys, LocalTimelineWithReplyToName(viewerID))
	}
	ids, err := s.fanout.GetMerged(ctx, keys, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		resolved, dangling, err := s.resolve(ctx, ids)
		if err != nil {
			return nil, err
		}
		s.pruneDangling(ctx, keys, dangling)
		notes := ApplyFilter(resolved, viewerID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			fbSince, fbUntil := fallbackRange(resolved, sinceID, untilID)
			rest, err := s.noteRepo.ListLocalTimeline(limit-len(notes), fbSince, fbUntil, dbFilter)
			if err != nil {
				return nil, err
			}
			return append(notes, rest...), nil
		}
		return notes, nil
	}
	return s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, dbFilter)
}

// GlobalTimeline returns all public notes including federated remotes.
func (s *Service) GlobalTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if limit <= 0 {
		limit = 20
	}
	viewerID := ""
	if viewer != nil {
		viewerID = viewer.ID
	}
	if !s.fanoutEnabled() {
		return s.noteRepo.ListGlobalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
	}
	ids, err := s.fanout.Get(ctx, GlobalTimeline, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		resolved, dangling, err := s.resolve(ctx, ids)
		if err != nil {
			return nil, err
		}
		s.pruneDangling(ctx, []Name{GlobalTimeline}, dangling)
		notes := ApplyFilter(resolved, viewerID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			fbSince, fbUntil := fallbackRange(resolved, sinceID, untilID)
			rest, err := s.noteRepo.ListGlobalTimeline(limit-len(notes), fbSince, fbUntil, toDBFilter(filter, viewerID))
			if err != nil {
				return nil, err
			}
			return append(notes, rest...), nil
		}
		return notes, nil
	}
	return s.noteRepo.ListGlobalTimeline(limit, sinceID, untilID, toDBFilter(filter, viewerID))
}

// HybridTimeline merges home and local timelines into a single feed.
func (s *Service) HybridTimeline(ctx context.Context, viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	if viewer == nil {
		return nil, ErrUnauthenticated
	}
	if limit <= 0 {
		limit = 20
	}
	// LTL 側は LocalTimeline 系列を返信の有無で使い分ける (LocalTimeline と
	// 同じ規則)。素の localTimeline には返信が入っていないので、ここで合流させ
	// ないと withReplies=true でも返信が出てこない。
	if !s.fanoutEnabled() {
		return s.hybridDBFallback(viewer, untilID, sinceID, limit, filter)
	}
	stlKeys := []Name{HomeTimelineName(viewer.ID), LocalTimeline}
	if filter.WithReplies != nil && *filter.WithReplies {
		stlKeys = append(stlKeys, LocalTimelineWithReplies)
	} else {
		stlKeys = append(stlKeys, LocalTimelineWithReplyToName(viewer.ID))
	}
	multi, err := s.fanout.GetMulti(ctx, stlKeys, untilID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	merged := mergeIDs(multi, limit)
	if len(merged) > 0 {
		resolved, dangling, err := s.resolve(ctx, merged)
		if err != nil {
			return nil, err
		}
		s.pruneDangling(ctx, stlKeys, dangling)
		notes := ApplyFilter(resolved, viewer.ID, filter)
		if !filter.AllowPartial && len(notes) < limit {
			fbSince, fbUntil := fallbackRange(resolved, sinceID, untilID)
			rest, err := s.hybridDBFallback(viewer, fbUntil, fbSince, limit-len(notes), filter)
			if err != nil {
				return nil, err
			}
			return append(notes, rest...), nil
		}
		return notes, nil
	}
	return s.hybridDBFallback(viewer, untilID, sinceID, limit, filter)
}

// hybridDBFallback queries the home and local timelines from the database and
// merges them. upstream Misskey TS と同 semantics: hybrid (= social) timeline
// は home (followee + 自分) と local (= 同 instance の public) の和集合を返す。
//
// 旧実装は ListHomeTimeline のみを呼んでおり、follow 関係が無い viewer に
// とって local public note (= 同 instance の他 user の public) が落ちていた
// (#819 で Playwright spec が detect)。本 helper は両 query 結果を ID 単位で
// dedup → ID 降順 sort → limit 截断する。pagination (sinceID/untilID) は両
// query に同じ値を渡すので merged 結果の boundary は upstream と一致する。
//
// 各 query は単独で limit 件まで返すので merged 後の最大件数は 2*limit、
// dedup と truncate を経て最終的に <= limit 件。逆に両 query の和が limit
// に届かなければ best-effort で limit 未満の結果を返す (= upstream parity、
// pagination は keyset 方式で次 page 取得時に補完される設計)。
func (s *Service) hybridDBFallback(viewer *model.User, untilID, sinceID string, limit int, filter TimelineFilter) ([]*model.Note, error) {
	dbFilter := toDBFilter(filter, viewer.ID)
	// upstream hybrid-timeline も withReplies (既定 false) が偽なら
	// 「返信ではない or 自己スレッド」に絞る。未指定 (nil) は false 扱い。
	if filter.WithReplies == nil || !*filter.WithReplies {
		dbFilter.ExcludeRepliesToOthers = true
	}
	homeNotes, err := s.noteRepo.ListHomeTimeline(viewer.ID, limit, sinceID, untilID, dbFilter)
	if err != nil {
		return nil, err
	}
	localNotes, err := s.noteRepo.ListLocalTimeline(limit, sinceID, untilID, dbFilter)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(homeNotes)+len(localNotes))
	out := make([]*model.Note, 0, len(homeNotes)+len(localNotes))
	for _, n := range homeNotes {
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		out = append(out, n)
	}
	for _, n := range localNotes {
		if _, dup := seen[n.ID]; dup {
			continue
		}
		seen[n.ID] = struct{}{}
		out = append(out, n)
	}
	// ID 降順 sort (= upstream と同じ keyset 順)。aidx は時系列で単調増加
	// するので ID 文字列の lexicographic 比較で十分。
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// toDBFilter converts a TimelineFilter to a model.TimelineDBFilter.
func toDBFilter(f TimelineFilter, viewerID string) model.TimelineDBFilter {
	return model.TimelineDBFilter{
		WithFiles:             f.WithFiles,
		WithRenotes:           f.WithRenotes,
		WithReplies:           f.WithReplies,
		IncludeMyRenotes:      f.IncludeMyRenotes,
		IncludeRenotedMyNotes: f.IncludeRenotedMyNotes,
		IncludeLocalRenotes:   f.IncludeLocalRenotes,
		ViewerID:              viewerID,
		MutedChannelIDs:       f.MutedChannelIDs,
		// FollowingIDs が配線されている経路 (HTL / STL) でだけ SQL 側の gate も
		// 有効にする。post-fetch だけだと DB fallback がすり抜ける。
		HideFollowersOnlyReplyFromNonFollowee: f.FollowingIDs != nil,
		// production の SQL 経路では muting テーブルへの subquery で filter
		// する (#894)。viewer 単位で bind parameter 数が固定 (2) なので
		// heavy-mute viewer (>1000 mute) でも planning コストが膨らまない。
		// Redis cache 経路 (in-memory ApplyFilter) は引き続き
		// TimelineFilter.MutedUserIDs (loadMutedUserIDs で事前取得) を使う。
		// MutedUserIDs literal は test override 用に残す (本 toDBFilter から
		// は伝搬しない)。
		UseMutingSubquery: viewerID != "",
		// renote-mute も同様に subquery 経路を使う (#903)。pure renote 条件
		// は applyTimelineFilter 側で組み立てる。
		UseRenoteMutingSubquery: viewerID != "",
		// 被block / instance-mute は loader で取得した literal list を両経路に
		// 渡す (#1681)。anon viewer では空。
		BlockerIDs:     f.BlockerIDs,
		MutedInstances: f.MutedInstances,
		// followed channel (mute 済除外) を home DB fallback に渡す (#1686)。
		// home 以外の timeline では handler が空のまま渡す。
		FollowedChannelIDs: f.FollowedChannelIDs,
	}
}

// resolve fetches notes from the repository preserving id ordering, and reports
// which ids resolved to nothing at all.
//
// **dangling を返すのは、呼び出し側が list から消せるようにするため** (#2715)。
// ephemeral の TTL が切れると note の実体だけが消えて ID が list に残り、以後
// 永久に解決できない。黙って落とすだけだと汚染が溜まり続ける。
//
// **確実に消えていると判った ID だけを返す。** ephemeral の lookup が失敗した
// ときは何も返さない — Redis の一時障害で生きている note の ID を消すと、
// 取り返しがつかない。
func (s *Service) resolve(ctx context.Context, ids []string) (notes []*model.Note, dangling []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	notes, err = s.noteRepo.FindManyByIDsWithUser(ids)
	if err != nil {
		return nil, nil, err
	}
	if len(notes) == len(ids) {
		return notes, nil, nil
	}
	if s.ephemeral == nil {
		// ephemeral を使わない構成では、DB に無い = 消えている。
		return notes, missingIDs(ids, notes), nil
	}

	// DB に無かった ID は ephemeral (リレー由来で未 materialize) かもしれない。
	found := make(map[string]struct{}, len(notes))
	for _, n := range notes {
		found[n.ID] = struct{}{}
	}
	missing := make([]string, 0, len(ids)-len(notes))
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}

	eph, err := s.ephemeral.GetNotes(ctx, missing)
	if err != nil {
		// Redis 障害で timeline 全体を落とさない。DB 分だけ返せば、呼び出し側の
		// 件数不足判定が DB fallback に倒してくれる。**dangling は返さない** —
		// 生きている note の ID を消しかねない。
		slog.WarnContext(ctx, "timeline: ephemeral lookup failed", "err", err)
		return notes, nil, nil
	}
	if len(eph) == 0 {
		return notes, missing, nil
	}

	merged := append(notes, eph...)
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID > merged[j].ID })
	return merged, missingIDs(ids, merged), nil
}

// missingIDs returns the ids that have no corresponding note, preserving the
// input order.
func missingIDs(ids []string, notes []*model.Note) []string {
	if len(notes) == 0 {
		return append([]string(nil), ids...)
	}
	found := make(map[string]struct{}, len(notes))
	for _, n := range notes {
		found[n.ID] = struct{}{}
	}
	// cap は計算しない。len(notes) > len(ids) は現状到達しないが、cap が負だと
	// panic するので、証明に依存しない形にしておく (#2718 review LOW-1)。
	var out []string
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// pruneDangling removes ids that resolve to nothing from the timelines they
// came from (#2715)。best-effort で、失敗しても読み取りには影響しない。
func (s *Service) pruneDangling(ctx context.Context, names []Name, dangling []string) {
	if s.fanout == nil || len(names) == 0 || len(dangling) == 0 {
		return
	}
	// **リクエストの ctx を持ち込まない。** クライアントが切断すると ctx が
	// キャンセルされ、pipeline が落ちて自己修復が空振りする。**症状が出るのは
	// リロード時 = 前のリクエストを中断する操作**なので、直したい場面ほど
	// 空振りしやすい (#2718 review MEDIUM-4)。
	ctx = context.WithoutCancel(ctx)
	slog.DebugContext(ctx, "timeline: pruning unresolvable ids", "count", len(dangling))
	if err := s.fanout.RemoveMany(ctx, names, dangling); err != nil {
		slog.WarnContext(ctx, "timeline: pruning unresolvable ids failed", "err", err)
	}
}

// mergeIDs flattens multiple ID slices, deduplicates, sorts id desc and caps.
func mergeIDs(slices [][]string, limit int) []string {
	seen := make(map[string]struct{})
	var all []string
	for _, s := range slices {
		for _, id := range s {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, id)
		}
	}
	// id降順
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[i] < all[j] {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}
