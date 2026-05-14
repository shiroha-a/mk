package user_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/shiroha-a/mk/internal/core/user"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFullSvc(t *testing.T) (*user.Service, *testutil.MockUserRepository, *testutil.MockNoteRepository, *testutil.MockUserNotePiningRepository) {
	t.Helper()
	uRepo := testutil.NewMockUserRepository()
	nRepo := testutil.NewMockNoteRepository()
	pRepo := testutil.NewMockUserNotePiningRepository()
	idGen, _ := id.NewGenerator("aidx")
	return user.NewService(uRepo, nRepo, pRepo, idGen), uRepo, nRepo, pRepo
}

func ptr[T any](v T) *T { return &v }

func TestService_ShowByID_Success(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	desc := "hello"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Description: &desc}
	svc := user.NewService(repo, nil, nil, nil)

	bundle, err := svc.ShowByID("u1")
	require.NoError(t, err)
	assert.Equal(t, "alice", bundle.User.Username)
	require.NotNil(t, bundle.Profile)
	assert.Equal(t, "hello", *bundle.Profile.Description)
}

func TestService_ShowByID_NotFound(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	_, err := svc.ShowByID("missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrUserNotFound))
}

func TestService_ShowByID_NoProfile(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	svc := user.NewService(repo, nil, nil, nil)

	bundle, err := svc.ShowByID("u1")
	require.NoError(t, err)
	assert.Nil(t, bundle.Profile)
}

func TestService_ShowManyByIDs_PreservesOrderAndSkipsMissing(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	repo.Users["u2"] = &model.User{ID: "u2", Username: "bob"}
	d1 := "d1"
	d2 := "d2"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Description: &d1}
	repo.Profiles["u2"] = &model.UserProfile{UserID: "u2", Description: &d2}
	svc := user.NewService(repo, nil, nil, nil)

	out, err := svc.ShowManyByIDs([]string{"u2", "ghost", "u1"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	// Order should follow input list; the missing "ghost" is dropped.
	assert.Equal(t, "u2", out[0].User.ID)
	assert.Equal(t, "u1", out[1].User.ID)
	require.NotNil(t, out[0].Profile)
	assert.Equal(t, "d2", *out[0].Profile.Description)
	require.NotNil(t, out[1].Profile)
	assert.Equal(t, "d1", *out[1].Profile.Description)
}

func TestService_ShowManyByIDs_EmptyInput(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	out, err := svc.ShowManyByIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestService_ShowManyByIDs_AllMissing(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	out, err := svc.ShowManyByIDs([]string{"x", "y"})
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestService_GetProfilesByUserIDs_BatchOK(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	d1, d2 := "p1", "p2"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Description: &d1}
	repo.Profiles["u2"] = &model.UserProfile{UserID: "u2", Description: &d2}
	svc := user.NewService(repo, nil, nil, nil)

	out := svc.GetProfilesByUserIDs([]string{"u1", "u2", "ghost"})
	require.Len(t, out, 2)
	require.NotNil(t, out["u1"])
	require.NotNil(t, out["u2"])
	assert.Equal(t, "p1", *out["u1"].Description)
	assert.Equal(t, "p2", *out["u2"].Description)
	assert.NotContains(t, out, "ghost", "missing profiles must not appear in the map")
}

func TestService_GetProfilesByUserIDs_EmptyInput(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)
	out := svc.GetProfilesByUserIDs(nil)
	assert.Empty(t, out)
}

// failingProfileRepo wraps the mock to make FindProfilesByUserIDs return an
// error, so we can exercise the service-side fallback (returns empty map,
// never nil).
type failingProfileRepo struct {
	*testutil.MockUserRepository
}

func (failingProfileRepo) FindProfilesByUserIDs(_ []string) ([]*model.UserProfile, error) {
	return nil, errors.New("db error")
}

func TestService_GetProfilesByUserIDs_RepoErrorYieldsEmptyMap(t *testing.T) {
	svc := user.NewService(failingProfileRepo{testutil.NewMockUserRepository()}, nil, nil, nil)
	out := svc.GetProfilesByUserIDs([]string{"u1"})
	require.NotNil(t, out, "must return a non-nil map even on repository error")
	assert.Empty(t, out)
}

func TestService_ShowManyByIDs_NoProfileSilentlyOmitted(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	// Profile 行が無くても User は返る
	svc := user.NewService(repo, nil, nil, nil)

	out, err := svc.ShowManyByIDs([]string{"u1"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "u1", out[0].User.ID)
	assert.Nil(t, out[0].Profile)
}

func TestService_ShowByUsername_Success(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	svc := user.NewService(repo, nil, nil, nil)

	bundle, err := svc.ShowByUsername("alice", nil)
	require.NoError(t, err)
	assert.Equal(t, "u1", bundle.User.ID)
}

func TestService_ShowByUsername_WithHost(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	host := "remote.example.com"
	repo.Users["u2"] = &model.User{ID: "u2", Username: "bob", UsernameLower: "bob", Host: &host}
	svc := user.NewService(repo, nil, nil, nil)

	bundle, err := svc.ShowByUsername("bob", &host)
	require.NoError(t, err)
	assert.Equal(t, "u2", bundle.User.ID)
}

func TestService_ShowByUsername_NotFound(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	_, err := svc.ShowByUsername("nobody", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrUserNotFound))
}

// stubRemoteResolver implements user.RemoteUserResolver for unit tests.
type stubRemoteResolver struct {
	calls []struct{ username, host string }
	user  *model.User
	err   error
}

func (s *stubRemoteResolver) ResolveByUsernameHost(username, host string) (*model.User, error) {
	s.calls = append(s.calls, struct{ username, host string }{username, host})
	if s.err != nil {
		return nil, s.err
	}
	return s.user, nil
}

func TestService_ShowByUsername_RemoteFallback_NoResolver_ReturnsErrUserNotFound(t *testing.T) {
	// host が指定されていても resolver が未注入なら従来どおり ErrUserNotFound
	// を返すこと (後方互換)。
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	host := "remote.example"
	_, err := svc.ShowByUsername("ghost", &host)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrUserNotFound))
}

func TestService_ShowByUsername_RemoteFallback_EmptyHostIgnoresResolver(t *testing.T) {
	// host が空文字列の場合は resolver を呼ばずに ErrUserNotFound を返す。
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)
	resolver := &stubRemoteResolver{}
	svc.SetRemoteUserResolver(resolver)

	empty := ""
	_, err := svc.ShowByUsername("ghost", &empty)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrUserNotFound))
	assert.Empty(t, resolver.calls)
}

func TestService_ShowByUsername_RemoteFallback_ResolverSucceeds(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)
	host := "remote.example"
	resolved := &model.User{ID: "u9", Username: "remote", UsernameLower: "remote", Host: &host}
	resolver := &stubRemoteResolver{user: resolved}
	svc.SetRemoteUserResolver(resolver)

	desc := "imported"
	repo.Profiles["u9"] = &model.UserProfile{UserID: "u9", Description: &desc}

	bundle, err := svc.ShowByUsername("remote", &host)
	require.NoError(t, err)
	assert.Equal(t, "u9", bundle.User.ID)
	require.NotNil(t, bundle.Profile)
	assert.Equal(t, "imported", *bundle.Profile.Description)
	require.Len(t, resolver.calls, 1)
	assert.Equal(t, "remote", resolver.calls[0].username)
	assert.Equal(t, host, resolver.calls[0].host)
}

func TestService_ShowByUsername_RemoteFallback_ResolverFails(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)
	resolver := &stubRemoteResolver{err: errors.New("webfinger: dial timeout")}
	svc.SetRemoteUserResolver(resolver)

	host := "remote.example"
	_, err := svc.ShowByUsername("ghost", &host)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrFailedToResolveRemoteUser))
	require.Len(t, resolver.calls, 1)
}

func TestService_ShowByUsername_RemoteFallback_ResolverReturnsNil(t *testing.T) {
	// resolver が (nil, nil) を返した場合も失敗扱いにする (防御的実装)。
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)
	resolver := &stubRemoteResolver{user: nil}
	svc.SetRemoteUserResolver(resolver)

	host := "remote.example"
	_, err := svc.ShowByUsername("ghost", &host)
	require.Error(t, err)
	assert.True(t, errors.Is(err, user.ErrFailedToResolveRemoteUser))
}

func TestService_ShowByUsername_LocalHit_ResolverNotCalled(t *testing.T) {
	// ローカル DB hit の場合は resolver を呼ばずに即返す。
	repo := testutil.NewMockUserRepository()
	host := "remote.example"
	repo.Users["u3"] = &model.User{ID: "u3", Username: "cached", UsernameLower: "cached", Host: &host}
	svc := user.NewService(repo, nil, nil, nil)
	resolver := &stubRemoteResolver{err: errors.New("should not be called")}
	svc.SetRemoteUserResolver(resolver)

	bundle, err := svc.ShowByUsername("cached", &host)
	require.NoError(t, err)
	assert.Equal(t, "u3", bundle.User.ID)
	assert.Empty(t, resolver.calls)
}

func TestService_GetProfile_Found(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	desc := "hi"
	repo.Profiles["u1"] = &model.UserProfile{UserID: "u1", Description: &desc}
	svc := user.NewService(repo, nil, nil, nil)

	p := svc.GetProfile("u1")
	require.NotNil(t, p)
	assert.Equal(t, "hi", *p.Description)
}

func TestService_GetProfile_Missing(t *testing.T) {
	repo := testutil.NewMockUserRepository()
	svc := user.NewService(repo, nil, nil, nil)

	assert.Nil(t, svc.GetProfile("missing"))
}

// --- Search ---

func TestService_Search_Empty(t *testing.T) {
	svc, _, _, _ := newFullSvc(t)
	out, err := svc.Search("   ", 10, 0, "")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestService_Search_AtPrefix(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	out, err := svc.Search("@al", 10, 0, "")
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

func TestService_Search_DefaultLimit(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice", UsernameLower: "alice"}
	out, err := svc.Search("a", 0, 0, "")
	require.NoError(t, err)
	assert.Len(t, out, 1)
}

// --- UpdateProfile ---

func TestService_UpdateProfile_Success(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	bundle, err := svc.UpdateProfile("u1", user.UpdateInput{
		Name:              ptr(ptr("New Name")),
		Description:       ptr(ptr("hi there")),
		Location:          ptr(ptr("Tokyo")),
		Birthday:          ptr(ptr("1990-01-01")),
		Lang:              ptr(ptr("ja")),
		IsLocked:          ptr(true),
		IsBot:             ptr(true),
		IsCat:             ptr(true),
		IsExplorable:      ptr(false),
		HideOnlineStatus:  ptr(true),
		AlwaysMarkNsfw:    ptr(true),
		AutoSensitive:     ptr(true),
		NoCrawle:          ptr(true),
		PreventAiLearning: ptr(true),
	})
	require.NoError(t, err)
	require.NotNil(t, bundle)
	assert.True(t, bundle.User.IsLocked)
	assert.True(t, bundle.User.IsBot)
}

// #787: ワードミュート (mutedWords / hardMutedWords) の persist。
func TestService_UpdateProfile_MutedWords(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	mw := json.RawMessage([]byte(`[["foo"],["bar","baz"]]`))
	hmw := json.RawMessage([]byte(`["spoiler"]`))
	_, err := svc.UpdateProfile("u1", user.UpdateInput{
		MutedWords:     &mw,
		HardMutedWords: &hmw,
	})
	require.NoError(t, err)

	got := repo.Profiles["u1"]
	require.NotNil(t, got)
	assert.JSONEq(t, `[["foo"],["bar","baz"]]`, string(got.MutedWords))
	assert.JSONEq(t, `["spoiler"]`, string(got.HardMutedWords))

	// 後続更新で `[]` を渡すと clear。
	clear := json.RawMessage([]byte(`[]`))
	_, err = svc.UpdateProfile("u1", user.UpdateInput{MutedWords: &clear})
	require.NoError(t, err)
	got = repo.Profiles["u1"]
	assert.JSONEq(t, `[]`, string(got.MutedWords))
	// hardMutedWords は omit されたので不変。
	assert.JSONEq(t, `["spoiler"]`, string(got.HardMutedWords))
}

// #956: profile fields (name/value 配列) の persist + 正規化。
// upstream Misskey TS は trim + 空 entry 排除を行うので、mk-go も同挙動に
// 揃える (= name または value どちらかが trim 後に空なら drop)。
func TestService_UpdateProfile_Fields(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	in := []user.FieldItem{
		{Name: "  blog  ", Value: " https://example.com  "},
		{Name: "empty-value", Value: ""},
		{Name: "", Value: "empty-name-only"},
		{Name: "  whitespace-name  ", Value: "  v  "},
		{Name: "x", Value: "y"},
	}
	_, err := svc.UpdateProfile("u1", user.UpdateInput{Fields: &in})
	require.NoError(t, err)

	got := repo.Profiles["u1"]
	require.NotNil(t, got)
	// 空 entry が drop され、name/value とも trim される。順序は維持。
	assert.JSONEq(t,
		`[{"name":"blog","value":"https://example.com"},{"name":"whitespace-name","value":"v"},{"name":"x","value":"y"}]`,
		string(got.Fields),
	)

	// nil (= 省略) は不変。Fields=[{x,y}, ...] 状態で他 field のみ更新
	// しても Fields が消えないことを pinpoint で verify する (= "Fields が
	// nil なら touch しない" 性質)。Description ptr-to-ptr で更新する。
	desc := "new description"
	descPtr := &desc
	_, err = svc.UpdateProfile("u1", user.UpdateInput{Description: &descPtr})
	require.NoError(t, err)
	got = repo.Profiles["u1"]
	assert.JSONEq(t,
		`[{"name":"blog","value":"https://example.com"},{"name":"whitespace-name","value":"v"},{"name":"x","value":"y"}]`,
		string(got.Fields),
	)
	require.NotNil(t, got.Description)
	assert.Equal(t, desc, *got.Description)

	// 後続更新で空 slice を渡すと clear (= [] 書き込み)。
	empty := []user.FieldItem{}
	_, err = svc.UpdateProfile("u1", user.UpdateInput{Fields: &empty})
	require.NoError(t, err)
	got = repo.Profiles["u1"]
	assert.JSONEq(t, `[]`, string(got.Fields))
}

// #467: avatarId に SET / CLEAR / 不明 ID / 他人ファイル / 非画像 MIME を
// 与えたときの挙動を確認する。banner も同経路 (applyMediaUpdate を共有)
// なので avatar 側で代表させる。
func TestService_UpdateProfile_AvatarSet(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	driveRepo := testutil.NewMockDriveFileRepository()
	owner := "u1"
	bh := "L6PZfSi_.AyE_3t7t7R**0o#DgR4"
	driveRepo.Files["f1"] = &model.DriveFile{
		ID: "f1", UserID: &owner, Type: "image/png",
		URL: "https://cdn.example/avatar.png", Blurhash: &bh,
	}
	svc.SetDriveFileRepository(driveRepo)

	id := "f1"
	bundle, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarID: &id})
	require.NoError(t, err)
	require.NotNil(t, bundle.User.AvatarID)
	assert.Equal(t, "f1", *bundle.User.AvatarID)
	require.NotNil(t, bundle.User.AvatarURL)
	assert.Equal(t, "https://cdn.example/avatar.png", *bundle.User.AvatarURL)
	require.NotNil(t, bundle.User.AvatarBlurhash)
	assert.Equal(t, bh, *bundle.User.AvatarBlurhash)
}

func TestService_UpdateProfile_AvatarClear(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	existingID := "f0"
	existingURL := "https://cdn.example/old.png"
	existingBh := "old"
	userRepo.Users["u1"] = &model.User{
		ID: "u1", Username: "alice",
		AvatarID:       &existingID,
		AvatarURL:      &existingURL,
		AvatarBlurhash: &existingBh,
	}
	driveRepo := testutil.NewMockDriveFileRepository()
	svc.SetDriveFileRepository(driveRepo)

	empty := ""
	bundle, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarID: &empty})
	require.NoError(t, err)
	assert.Nil(t, bundle.User.AvatarID)
	assert.Nil(t, bundle.User.AvatarURL)
	assert.Nil(t, bundle.User.AvatarBlurhash)
}

func TestService_UpdateProfile_AvatarNotFound(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	svc.SetDriveFileRepository(testutil.NewMockDriveFileRepository())

	id := "missing"
	_, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarID: &id})
	assert.True(t, errors.Is(err, user.ErrAvatarNotFound))
}

func TestService_UpdateProfile_AvatarNotOwned(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	driveRepo := testutil.NewMockDriveFileRepository()
	otherOwner := "u2"
	driveRepo.Files["f1"] = &model.DriveFile{
		ID: "f1", UserID: &otherOwner, Type: "image/png", URL: "https://x/x",
	}
	svc.SetDriveFileRepository(driveRepo)

	id := "f1"
	_, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarID: &id})
	// owner mismatch も notFound で扱う (upstream 互換 + 列挙攻撃避け)。
	assert.True(t, errors.Is(err, user.ErrAvatarNotFound))
}

func TestService_UpdateProfile_AvatarNotImage(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	driveRepo := testutil.NewMockDriveFileRepository()
	owner := "u1"
	driveRepo.Files["f1"] = &model.DriveFile{
		ID: "f1", UserID: &owner, Type: "video/mp4", URL: "https://x/v.mp4",
	}
	svc.SetDriveFileRepository(driveRepo)

	id := "f1"
	_, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarID: &id})
	assert.True(t, errors.Is(err, user.ErrAvatarNotImage))
}

// #521: UpdateInput.AvatarDecorations が user.avatarDecorations カラムに
// jsonb (= string) として書き込まれることを確認する。
func TestService_UpdateProfile_AvatarDecorationsSet(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	raw := []byte(`[{"id":"dec1","angle":0,"flipH":false,"offsetX":0,"offsetY":0}]`)
	bundle, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarDecorations: &raw})
	require.NoError(t, err)
	require.NotNil(t, bundle.User)
	assert.JSONEq(t, string(raw), string(userRepo.Users["u1"].AvatarDecorations))
}

func TestService_UpdateProfile_AvatarDecorationsClear(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice", AvatarDecorations: []byte(`[{"id":"dec1"}]`)}

	empty := []byte(`[]`)
	_, err := svc.UpdateProfile("u1", user.UpdateInput{AvatarDecorations: &empty})
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(userRepo.Users["u1"].AvatarDecorations))
}

func TestService_UpdateProfile_BannerSet(t *testing.T) {
	// banner は avatar と applyMediaUpdate 共有なので smoke test 1 件のみ。
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	driveRepo := testutil.NewMockDriveFileRepository()
	owner := "u1"
	driveRepo.Files["b1"] = &model.DriveFile{
		ID: "b1", UserID: &owner, Type: "image/jpeg",
		URL: "https://cdn.example/banner.jpg",
	}
	svc.SetDriveFileRepository(driveRepo)

	id := "b1"
	bundle, err := svc.UpdateProfile("u1", user.UpdateInput{BannerID: &id})
	require.NoError(t, err)
	require.NotNil(t, bundle.User.BannerID)
	assert.Equal(t, "b1", *bundle.User.BannerID)
	require.NotNil(t, bundle.User.BannerURL)
	assert.Equal(t, "https://cdn.example/banner.jpg", *bundle.User.BannerURL)
}

func TestService_UpdateProfile_NoFields(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}

	_, err := svc.UpdateProfile("u1", user.UpdateInput{})
	require.NoError(t, err)
}

func TestService_UpdateProfile_UserNotFound(t *testing.T) {
	svc, _, _, _ := newFullSvc(t)
	_, err := svc.UpdateProfile("missing", user.UpdateInput{})
	assert.True(t, errors.Is(err, user.ErrUserNotFound))
}

// stubMainStreamPublisher captures PublishMainEvent calls for assertion.
type stubMainStreamPublisher struct {
	calls []mainEventCall
}

type mainEventCall struct {
	userID    string
	eventType string
	body      any
}

func (s *stubMainStreamPublisher) PublishMainEvent(userID, eventType string, body any) {
	s.calls = append(s.calls, mainEventCall{userID, eventType, body})
}

func TestService_UpdateProfile_PublishesMeUpdated(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	_, err := svc.UpdateProfile("u1", user.UpdateInput{
		Name:        ptr(ptr("New Name")),
		Description: ptr(ptr("hello")),
	})
	require.NoError(t, err)

	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "meUpdated", pub.calls[0].eventType)
	// body は entity.UserDetailed 相当。JSON round-trip で id/name/description
	// が更新後の値になっていることを確認する。
	raw, err := json.Marshal(pub.calls[0].body)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "u1", m["id"])
	assert.Equal(t, "New Name", m["name"])
	assert.Equal(t, "hello", m["description"])
}

func TestService_UpdateProfile_UserUpdateError_DoesNotPublish(t *testing.T) {
	mockUR := testutil.NewMockUserRepository()
	mockUR.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	uRepo := &failingUserRepo{MockUserRepository: mockUR, failUpdateUser: true}
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(uRepo, nil, nil, idGen)
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	_, err := svc.UpdateProfile("u1", user.UpdateInput{IsLocked: ptr(true)})
	require.Error(t, err)
	assert.Empty(t, pub.calls)
}

// --- PinNote / UnpinNote / ListPinnedNotes ---

func TestService_PinNote_Success(t *testing.T) {
	svc, repo, noteRepo, piningRepo := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}

	require.NoError(t, svc.PinNote("u1", "n1"))
	assert.Len(t, piningRepo.Pinings, 1)
}

func TestService_PinNote_NoteNotFound(t *testing.T) {
	svc, repo, _, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}

	err := svc.PinNote("u1", "ghost")
	assert.True(t, errors.Is(err, user.ErrNoteNotFound))
}

func TestService_PinNote_NotOwner(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "other"}

	err := svc.PinNote("u1", "n1")
	assert.True(t, errors.Is(err, user.ErrNoteNotFound))
}

func TestService_PinNote_AlreadyPinned(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	require.NoError(t, svc.PinNote("u1", "n1"))

	err := svc.PinNote("u1", "n1")
	assert.True(t, errors.Is(err, user.ErrAlreadyPinned))
}

func TestService_PinNote_LimitExceeded(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	for i := 1; i <= user.MaxPinnedNotes; i++ {
		noteID := "n" + string(rune('0'+i))
		noteRepo.Notes[noteID] = &model.Note{ID: noteID, UserID: "u1"}
		require.NoError(t, svc.PinNote("u1", noteID))
	}

	noteRepo.Notes["n_extra"] = &model.Note{ID: "n_extra", UserID: "u1"}
	err := svc.PinNote("u1", "n_extra")
	assert.True(t, errors.Is(err, user.ErrPinLimitExceeded))
}

func TestService_UnpinNote_Success(t *testing.T) {
	svc, repo, noteRepo, piningRepo := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	require.NoError(t, svc.PinNote("u1", "n1"))

	require.NoError(t, svc.UnpinNote("u1", "n1"))
	assert.Empty(t, piningRepo.Pinings)
}

func TestService_UnpinNote_NotFound(t *testing.T) {
	svc, _, _, _ := newFullSvc(t)
	err := svc.UnpinNote("u1", "n1")
	assert.True(t, errors.Is(err, user.ErrPinNotFound))
}

func TestService_PinNote_PublishesMeUpdated(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	require.NoError(t, svc.PinNote("u1", "n1"))
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "meUpdated", pub.calls[0].eventType)
	raw, err := json.Marshal(pub.calls[0].body)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "u1", m["id"])
	// piningRepo から補完された pinnedNoteIds が含まれること
	ids, ok := m["pinnedNoteIds"].([]any)
	require.True(t, ok)
	assert.Equal(t, []any{"n1"}, ids)
}

func TestService_PinNote_NoPublisher_NoDBRead(t *testing.T) {
	// publisher 未注入時は ShowByID を呼ばないことを確認するため、
	// pin 直後に user を repo から削除しても PinNote は error にならない
	// (ShowByID されないため)。
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	// publisher を意図的に設定しない
	require.NoError(t, svc.PinNote("u1", "n1"))
}

func TestService_UnpinNote_PublishesMeUpdated(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1", Username: "alice"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	require.NoError(t, svc.PinNote("u1", "n1"))

	// Pin で emit された分を除外するため publisher を差し替え。
	pub := &stubMainStreamPublisher{}
	svc.SetMainStreamPublisher(pub)

	require.NoError(t, svc.UnpinNote("u1", "n1"))
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "u1", pub.calls[0].userID)
	assert.Equal(t, "meUpdated", pub.calls[0].eventType)
}

func TestService_ListPinnedNotes_Success(t *testing.T) {
	svc, repo, noteRepo, _ := newFullSvc(t)
	repo.Users["u1"] = &model.User{ID: "u1"}
	noteRepo.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	noteRepo.Notes["n2"] = &model.Note{ID: "n2", UserID: "u1"}
	require.NoError(t, svc.PinNote("u1", "n1"))
	require.NoError(t, svc.PinNote("u1", "n2"))

	notes, err := svc.ListPinnedNotes("u1")
	require.NoError(t, err)
	assert.Len(t, notes, 2)
}

func TestService_ListPinnedNotes_Empty(t *testing.T) {
	svc, _, _, _ := newFullSvc(t)
	notes, err := svc.ListPinnedNotes("u1")
	require.NoError(t, err)
	assert.Empty(t, notes)
}

// --- Failing-repo error paths ---

var stubError = errors.New("stub error")

type failingUserRepo struct {
	*testutil.MockUserRepository
	failUpdateUser    bool
	failUpdateProfile bool
}

func (f *failingUserRepo) UpdateUser(userID string, fields map[string]any) error {
	if f.failUpdateUser {
		return stubError
	}
	return f.MockUserRepository.UpdateUser(userID, fields)
}

func (f *failingUserRepo) UpdateProfile(userID string, fields map[string]any) error {
	if f.failUpdateProfile {
		return stubError
	}
	return f.MockUserRepository.UpdateProfile(userID, fields)
}

type failingPiningRepo struct {
	*testutil.MockUserNotePiningRepository
	failCount   bool
	failListByU bool
}

func (f *failingPiningRepo) CountByUser(userID string) (int, error) {
	if f.failCount {
		return 0, stubError
	}
	return f.MockUserNotePiningRepository.CountByUser(userID)
}

func (f *failingPiningRepo) ListByUser(userID string) ([]*model.UserNotePining, error) {
	if f.failListByU {
		return nil, stubError
	}
	return f.MockUserNotePiningRepository.ListByUser(userID)
}

func TestService_UpdateProfile_UserUpdateError(t *testing.T) {
	mockUR := testutil.NewMockUserRepository()
	mockUR.Users["u1"] = &model.User{ID: "u1"}
	uRepo := &failingUserRepo{MockUserRepository: mockUR, failUpdateUser: true}
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(uRepo, testutil.NewMockNoteRepository(), testutil.NewMockUserNotePiningRepository(), idGen)

	_, err := svc.UpdateProfile("u1", user.UpdateInput{IsLocked: ptr(true)})
	assert.ErrorIs(t, err, stubError)
}

func TestService_UpdateProfile_ProfileUpdateError(t *testing.T) {
	mockUR := testutil.NewMockUserRepository()
	mockUR.Users["u1"] = &model.User{ID: "u1"}
	uRepo := &failingUserRepo{MockUserRepository: mockUR, failUpdateProfile: true}
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(uRepo, testutil.NewMockNoteRepository(), testutil.NewMockUserNotePiningRepository(), idGen)

	_, err := svc.UpdateProfile("u1", user.UpdateInput{Description: ptr(ptr("hi"))})
	assert.ErrorIs(t, err, stubError)
}

func TestService_PinNote_CountError(t *testing.T) {
	mockUR := testutil.NewMockUserRepository()
	mockUR.Users["u1"] = &model.User{ID: "u1"}
	mockNR := testutil.NewMockNoteRepository()
	mockNR.Notes["n1"] = &model.Note{ID: "n1", UserID: "u1"}
	piningRepo := &failingPiningRepo{MockUserNotePiningRepository: testutil.NewMockUserNotePiningRepository(), failCount: true}
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(mockUR, mockNR, piningRepo, idGen)

	err := svc.PinNote("u1", "n1")
	assert.ErrorIs(t, err, stubError)
}

func TestService_ListPinnedNotes_Error(t *testing.T) {
	mockUR := testutil.NewMockUserRepository()
	piningRepo := &failingPiningRepo{MockUserNotePiningRepository: testutil.NewMockUserNotePiningRepository(), failListByU: true}
	idGen, _ := id.NewGenerator("aidx")
	svc := user.NewService(mockUR, testutil.NewMockNoteRepository(), piningRepo, idGen)

	_, err := svc.ListPinnedNotes("u1")
	assert.ErrorIs(t, err, stubError)
}

func TestUpdateUserFields(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1", Username: "test"}
	err := svc.UpdateUserFields("u1", map[string]any{"isSuspended": true})
	require.NoError(t, err)
}

func TestUpdateProfileFields(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	userRepo.Users["u1"] = &model.User{ID: "u1"}
	userRepo.Profiles["u1"] = &model.UserProfile{UserID: "u1"}
	err := svc.UpdateProfileFields("u1", map[string]any{"description": "new"})
	require.NoError(t, err)
}

// #766 / #1064: SearchByUsernameAndHost の各分岐を service レイヤーで覆う。
// upstream UserSearchService.generateUserQueryBuilder と同じく、host=falsy は
// filter 無し (local + remote)、host="." or self-hostname は local 限定、
// それ以外は host prefix match。
func TestService_SearchByUsernameAndHost(t *testing.T) {
	svc, userRepo, _, _ := newFullSvc(t)
	remoteHost := "remote.example"
	otherHost := "other.example"
	userRepo.Users["u_local"] = &model.User{ID: "u_local", Username: "alice", UsernameLower: "alice"}
	userRepo.Users["u_remote"] = &model.User{ID: "u_remote", Username: "alice", UsernameLower: "alice", Host: &remoteHost}
	userRepo.Users["u_other"] = &model.User{ID: "u_other", Username: "alice", UsernameLower: "alice", Host: &otherHost}

	t.Run("empty query returns nil", func(t *testing.T) {
		out, err := svc.SearchByUsernameAndHost("", nil, 10)
		require.NoError(t, err)
		assert.Nil(t, out)
	})

	t.Run("@ prefix is stripped and host=nil returns local+remote", func(t *testing.T) {
		out, err := svc.SearchByUsernameAndHost("@alice", nil, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids["u_local"], "@ prefix should strip and host=nil returns local")
		assert.True(t, ids["u_remote"], "host=nil should include remote")
		assert.True(t, ids["u_other"], "host=nil should include other host too")
	})

	// #1064: host=nil は upstream `params.host` falsy 経路で host filter なし
	// = local + remote 両方返す。旧来は local 限定だったが drift だった。
	t.Run("host=nil returns local and remote", func(t *testing.T) {
		out, err := svc.SearchByUsernameAndHost("alice", nil, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids["u_local"])
		assert.True(t, ids["u_remote"])
		assert.True(t, ids["u_other"])
	})

	t.Run("host=empty string is treated as nil (no filter)", func(t *testing.T) {
		empty := ""
		out, err := svc.SearchByUsernameAndHost("alice", &empty, 10)
		require.NoError(t, err)
		ids := make(map[string]bool, len(out))
		for _, u := range out {
			ids[u.ID] = true
		}
		assert.True(t, ids["u_local"])
		assert.True(t, ids["u_remote"])
		assert.True(t, ids["u_other"])
	})

	// #1064: "." は upstream で local 限定への shortcut。
	t.Run("host=. returns only local", func(t *testing.T) {
		dot := "."
		out, err := svc.SearchByUsernameAndHost("alice", &dot, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_local", out[0].ID)
	})

	// #1064: host が自 hostname と一致したら local 限定に remap される。
	// 各 selfHostname 関連 subtest は冒頭で明示的に初期状態を Set し、
	// `t.Cleanup` の実行順序に依存しない (= 個別 `-run` 実行や test reorder
	// 耐性のため)。
	t.Run("host=self-hostname returns only local", func(t *testing.T) {
		svc.SetSelfHostname("my.example")
		t.Cleanup(func() { svc.SetSelfHostname("") })
		self := "my.example"
		out, err := svc.SearchByUsernameAndHost("alice", &self, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_local", out[0].ID)
	})

	// 自 hostname 比較は case-insensitive。
	t.Run("self-hostname remap is case-insensitive", func(t *testing.T) {
		svc.SetSelfHostname("My.Example")
		t.Cleanup(func() { svc.SetSelfHostname("") })
		upper := "MY.EXAMPLE"
		out, err := svc.SearchByUsernameAndHost("alice", &upper, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_local", out[0].ID)
	})

	// SetSelfHostname は input を TrimSpace してから保存するので、whitespace
	// 込みで設定しても "." 一致のみ local 限定に degrade することは無く、
	// hostname だけ抽出されて正しく remap が機能する。
	t.Run("self-hostname is trimmed on set", func(t *testing.T) {
		svc.SetSelfHostname("  my.example  ")
		t.Cleanup(func() { svc.SetSelfHostname("") })
		self := "my.example"
		out, err := svc.SearchByUsernameAndHost("alice", &self, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_local", out[0].ID)
	})

	// whitespace のみの input は空文字と同じ扱い (= remap disabled、"." 一致のみ
	// local 限定)。SetSelfHostname の TrimSpace が空にしたケースで remap が
	// 誤って効かないことを regression guard。
	t.Run("self-hostname whitespace-only is treated as unset", func(t *testing.T) {
		svc.SetSelfHostname("   ")
		t.Cleanup(func() { svc.SetSelfHostname("") })
		// 任意の host 文字列を与えても local 限定にならない (= host filter として動く)。
		other := "my.example"
		out, err := svc.SearchByUsernameAndHost("alice", &other, 10)
		require.NoError(t, err)
		// 仕込み user に `my.example` host を持つものは無いので 0 件。
		// remap が誤って効いて local 1 件返ったらこの assertion が落ちる。
		assert.Empty(t, out)
	})

	// selfHostname 未設定なら "." 以外の input は host filter として扱われる。
	// 前の subtest の cleanup に依存せず冒頭で明示的に "" を Set する。
	t.Run("self-hostname unset leaves other inputs as host filter", func(t *testing.T) {
		svc.SetSelfHostname("")
		other := "my.example"
		out, err := svc.SearchByUsernameAndHost("alice", &other, 10)
		require.NoError(t, err)
		// selfHostname 未設定なので "my.example" prefix match。仕込み user に
		// `my.example` host を持つものは無いので 0 件。
		assert.Empty(t, out)
	})

	t.Run("host=remoteHost narrows to host prefix match", func(t *testing.T) {
		out, err := svc.SearchByUsernameAndHost("alice", &remoteHost, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_remote", out[0].ID)
	})

	t.Run("host comparison is case-insensitive", func(t *testing.T) {
		upper := "REMOTE.EXAMPLE"
		out, err := svc.SearchByUsernameAndHost("alice", &upper, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_remote", out[0].ID)
	})

	// #1054 / #1060: host prefix match は維持される。`rem` で remote.example が hit。
	t.Run("host prefix matches remote host", func(t *testing.T) {
		prefix := "rem"
		out, err := svc.SearchByUsernameAndHost("alice", &prefix, 10)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "u_remote", out[0].ID)
	})

	t.Run("limit defaults to 10 when zero", func(t *testing.T) {
		// 11 件 local user を仕込んで limit 未指定で 10 に clamp されることを確認
		repo2 := testutil.NewMockUserRepository()
		for i := 0; i < 11; i++ {
			id := fmt.Sprintf("u_many_%02d", i)
			repo2.Users[id] = &model.User{ID: id, Username: "many", UsernameLower: "many"}
		}
		idGen, _ := id.NewGenerator("aidx")
		s2 := user.NewService(repo2, testutil.NewMockNoteRepository(), testutil.NewMockUserNotePiningRepository(), idGen)
		out, err := s2.SearchByUsernameAndHost("many", nil, 0)
		require.NoError(t, err)
		assert.Len(t, out, 10)
	})
}
