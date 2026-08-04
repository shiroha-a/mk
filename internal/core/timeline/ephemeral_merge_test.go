package timeline

import (
	"context"
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEphemeral serves notes that only exist in Redis (#2332).
type stubEphemeral struct {
	notes    map[string]*model.Note
	err      error
	askedFor []string
}

func (s *stubEphemeral) GetNotes(_ context.Context, ids []string) ([]*model.Note, error) {
	s.askedFor = append(s.askedFor, ids...)
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*model.Note, 0, len(ids))
	for _, id := range ids {
		if n, ok := s.notes[id]; ok {
			out = append(out, n)
		}
	}
	return out, nil
}

func ephNote(id string) *model.Note {
	host := "remote.example"
	return &model.Note{
		ID: id, UserID: "ru", UserHost: &host,
		Visibility: model.NoteVisibilityPublic,
		User:       &model.User{ID: "ru", Host: &host},
	}
}

// newMergeService builds a Service whose fanout returns the given IDs and
// whose note repo holds only dbIDs.
func newMergeService(t *testing.T, dbIDs []string) (*Service, *testutil.MockNoteRepository) {
	t.Helper()
	repo := testutil.NewMockNoteRepository()
	for _, id := range dbIDs {
		repo.Notes[id] = &model.Note{ID: id, UserID: "lu", Visibility: model.NoteVisibilityPublic}
	}
	return NewService(nil, repo, testutil.NewMockFollowingRepository()), repo
}

func TestResolve_MergesEphemeralNotes(t *testing.T) {
	svc, _ := newMergeService(t, []string{"b"})
	eph := &stubEphemeral{notes: map[string]*model.Note{"a": ephNote("a"), "c": ephNote("c")}}
	svc.SetEphemeralLookup(eph)

	got, err := svc.resolve(context.Background(), []string{"c", "b", "a"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	// id 降順に並ぶこと (timeline の並び順)。
	assert.Equal(t, []string{"c", "b", "a"}, []string{got[0].ID, got[1].ID, got[2].ID})
	// DB で見つかった分は Redis に問い合わせない。
	assert.ElementsMatch(t, []string{"a", "c"}, eph.askedFor)
}

func TestResolve_NoEphemeralLookupWired(t *testing.T) {
	svc, _ := newMergeService(t, []string{"b"})

	got, err := svc.resolve(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].ID)
}

// 全件 DB で見つかったら Redis は引かない (ホットパスの無駄打ち防止)。
func TestResolve_SkipsLookupWhenAllFound(t *testing.T) {
	svc, _ := newMergeService(t, []string{"a", "b"})
	eph := &stubEphemeral{notes: map[string]*model.Note{}}
	svc.SetEphemeralLookup(eph)

	got, err := svc.resolve(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Empty(t, eph.askedFor, "DB で揃っていれば Redis を引かない")
}

// Redis 障害で timeline 全体を落とさない。DB 分だけ返す。
func TestResolve_EphemeralErrorDegradesToDB(t *testing.T) {
	svc, _ := newMergeService(t, []string{"b"})
	svc.SetEphemeralLookup(&stubEphemeral{err: errors.New("redis down")})

	got, err := svc.resolve(context.Background(), []string{"a", "b"})
	require.NoError(t, err, "Redis 障害は error にせず縮退する")
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].ID)
}

// ephemeral 側にも無い ID は単に落ちる (TTL 切れ後の dangling ID)。
func TestResolve_MissingEverywhere(t *testing.T) {
	svc, _ := newMergeService(t, []string{"b"})
	svc.SetEphemeralLookup(&stubEphemeral{notes: map[string]*model.Note{}})

	got, err := svc.resolve(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].ID)
}

func TestResolve_EmptyIDs(t *testing.T) {
	svc, _ := newMergeService(t, nil)
	svc.SetEphemeralLookup(&stubEphemeral{})

	got, err := svc.resolve(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ephemeral note も ApplyFilter を通ること (ブロック / ミュートが効く)。
func TestResolve_EphemeralNotesGoThroughFilter(t *testing.T) {
	svc, _ := newMergeService(t, nil)
	svc.SetEphemeralLookup(&stubEphemeral{notes: map[string]*model.Note{"a": ephNote("a")}})

	got, err := svc.resolve(context.Background(), []string{"a"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	// ApplyFilter は *model.Note を受け取るので ephemeral でもそのまま通せる。
	filtered := ApplyFilter(got, "viewer", TimelineFilter{WithRenotes: boolPtr(false)})
	assert.NotNil(t, filtered)
}
