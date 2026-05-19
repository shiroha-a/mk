package page_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/page"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSvc(t *testing.T) (*page.Service, *testutil.MockPageRepository, *testutil.MockPageLikeRepository) {
	t.Helper()
	repo := testutil.NewMockPageRepository()
	likeRepo := testutil.NewMockPageLikeRepository()
	idGen, _ := id.NewGenerator("aidx")
	return page.NewService(repo, likeRepo, idGen), repo, likeRepo
}

// --- Create ----------------------------------------------------------------

func TestCreate_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	p, err := svc.Create(page.CreateInput{
		OwnerID: "u1", Title: "t", Name: "alpha",
	})
	require.NoError(t, err)
	assert.Equal(t, "alpha", p.Name)
	assert.Equal(t, model.PageVisibilityPublic, p.Visibility)
	assert.Equal(t, "sans-serif", p.Font)
	assert.Equal(t, []byte("[]"), []byte(p.Content))
	assert.Equal(t, []byte("[]"), []byte(p.Variables))
	assert.Len(t, repo.Pages, 1)
}

func TestCreate_PassesThroughOptionalFields(t *testing.T) {
	svc, _, _ := newSvc(t)
	summary := "summary"
	imgID := "img1"
	p, err := svc.Create(page.CreateInput{
		OwnerID:             "u1",
		Title:               "t",
		Name:                "alpha",
		Summary:             &summary,
		AlignCenter:         true,
		HideTitleWhenPinned: true,
		Font:                "serif",
		EyeCatchingImageID:  &imgID,
		Content:             []byte(`[{"id":"a"}]`),
		Variables:           []byte(`[{"v":1}]`),
		Script:              "x",
		Visibility:          model.PageVisibilityFollowers,
	})
	require.NoError(t, err)
	assert.Equal(t, "summary", *p.Summary)
	assert.True(t, p.AlignCenter)
	assert.True(t, p.HideTitleWhenPinned)
	assert.Equal(t, "serif", p.Font)
	assert.Equal(t, "img1", *p.EyeCatchingImageID)
	assert.Equal(t, "x", p.Script)
	assert.Equal(t, model.PageVisibilityFollowers, p.Visibility)
}

func TestCreate_OwnerRequired(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Create(page.CreateInput{Title: "t", Name: "alpha"})
	assert.Error(t, err)
}

func TestCreate_TitleRequired(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Create(page.CreateInput{OwnerID: "u1", Name: "alpha"})
	assert.ErrorIs(t, err, page.ErrPageTitleRequired)
}

func TestCreate_NameRequired(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Create(page.CreateInput{OwnerID: "u1", Title: "t"})
	assert.ErrorIs(t, err, page.ErrPageNameRequired)
}

func TestCreate_NameConflict(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["existing"] = &model.Page{ID: "existing", UserID: "u1", Name: "alpha"}
	_, err := svc.Create(page.CreateInput{OwnerID: "u1", Title: "t", Name: "alpha"})
	assert.ErrorIs(t, err, page.ErrPageNameConflict)
}

func TestCreate_RepoError(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.CreateErr = errors.New("boom")
	_, err := svc.Create(page.CreateInput{OwnerID: "u1", Title: "t", Name: "alpha"})
	assert.Error(t, err)
}

// --- Show ------------------------------------------------------------------

func TestShow_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	got, err := svc.Show("u2", "p1")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.ID)
}

func TestShow_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Show("u1", "missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestShow_PrivateAccessDenied(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityFollowers}
	_, err := svc.Show("u2", "p1")
	assert.ErrorIs(t, err, page.ErrAccessDenied)
}

func TestShow_PrivateOwnerOK(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityFollowers}
	got, err := svc.Show("u1", "p1")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.ID)
}

// --- FindByID (visibility チェック無し) -----------------------------------

func TestFindByID_HappyPath_Public(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	got, err := svc.FindByID("p1")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.ID)
}

func TestFindByID_Returns_Private_Regardless_Of_Visibility(t *testing.T) {
	svc, repo, _ := newSvc(t)
	// followers visibilityでも visibility checkはせず返すことを確認
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityFollowers}
	got, err := svc.FindByID("p1")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.ID)
}

func TestFindByID_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.FindByID("missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

// --- ShowByName ------------------------------------------------------------

func TestShowByName_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Name: "alpha", Visibility: model.PageVisibilityPublic}
	got, err := svc.ShowByName("u2", "u1", "alpha")
	require.NoError(t, err)
	assert.Equal(t, "p1", got.ID)
}

func TestShowByName_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.ShowByName("u1", "u1", "missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestShowByName_PrivateAccessDenied(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Name: "alpha", Visibility: model.PageVisibilityFollowers}
	_, err := svc.ShowByName("u2", "u1", "alpha")
	assert.ErrorIs(t, err, page.ErrAccessDenied)
}

// --- Update ----------------------------------------------------------------

func TestUpdate_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Name: "alpha", Title: "t"}
	newTitle := "t2"
	newName := "beta"
	summary := "s"
	summaryPtr := &summary
	font := "serif"
	imgID := "img1"
	imgPtr := &imgID
	align := true
	hide := true
	script := "x"
	vis := model.PageVisibilityFollowers
	got, err := svc.Update("u1", "p1", page.UpdateInput{
		Title:               &newTitle,
		Name:                &newName,
		Summary:             &summaryPtr,
		AlignCenter:         &align,
		HideTitleWhenPinned: &hide,
		Font:                &font,
		EyeCatchingImageID:  &imgPtr,
		Content:             []byte("[1]"),
		Variables:           []byte("[2]"),
		Script:              &script,
		Visibility:          &vis,
	})
	require.NoError(t, err)
	assert.Equal(t, "t2", got.Title)
	assert.Equal(t, "beta", got.Name)
	require.NotNil(t, got.Summary)
	assert.Equal(t, "s", *got.Summary)
	assert.True(t, got.AlignCenter)
	assert.True(t, got.HideTitleWhenPinned)
	assert.Equal(t, "serif", got.Font)
	require.NotNil(t, got.EyeCatchingImageID)
	assert.Equal(t, "img1", *got.EyeCatchingImageID)
	assert.Equal(t, "x", got.Script)
	assert.Equal(t, model.PageVisibilityFollowers, got.Visibility)
}

func TestUpdate_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Update("u1", "missing", page.UpdateInput{})
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestUpdate_AccessDenied(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "owner"}
	_, err := svc.Update("u1", "p1", page.UpdateInput{})
	assert.ErrorIs(t, err, page.ErrAccessDenied)
}

func TestUpdate_TitleEmpty(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1"}
	empty := ""
	_, err := svc.Update("u1", "p1", page.UpdateInput{Title: &empty})
	assert.ErrorIs(t, err, page.ErrPageTitleRequired)
}

func TestUpdate_NameEmpty(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1"}
	empty := ""
	_, err := svc.Update("u1", "p1", page.UpdateInput{Name: &empty})
	assert.ErrorIs(t, err, page.ErrPageNameRequired)
}

func TestUpdate_NameUnchangedNoConflictCheck(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Name: "alpha"}
	name := "alpha"
	_, err := svc.Update("u1", "p1", page.UpdateInput{Name: &name})
	require.NoError(t, err)
}

func TestUpdate_NameConflict(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Name: "alpha"}
	repo.Pages["p2"] = &model.Page{ID: "p2", UserID: "u1", Name: "beta"}
	name := "beta"
	_, err := svc.Update("u1", "p1", page.UpdateInput{Name: &name})
	assert.ErrorIs(t, err, page.ErrPageNameConflict)
}

func TestUpdate_RepoError(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1"}
	repo.UpdateErr = errors.New("boom")
	title := "x"
	_, err := svc.Update("u1", "p1", page.UpdateInput{Title: &title})
	assert.Error(t, err)
}

// --- Delete ----------------------------------------------------------------

func TestDelete_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1"}
	require.NoError(t, svc.Delete("u1", "p1"))
	assert.Empty(t, repo.Pages)
}

func TestDelete_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Delete("u1", "missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestDelete_AccessDenied(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "owner"}
	err := svc.Delete("u1", "p1")
	assert.ErrorIs(t, err, page.ErrAccessDenied)
}

// --- ListByUser / Featured ------------------------------------------------

func TestListByUser(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1"}
	repo.Pages["p2"] = &model.Page{ID: "p2", UserID: "u1"}
	rows, err := svc.ListByUser("u1", "", "", 10, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestFeatured(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic, LikedCount: 5}
	repo.Pages["p2"] = &model.Page{ID: "p2", UserID: "u1", Visibility: model.PageVisibilityPublic, LikedCount: 10}
	repo.Pages["p3"] = &model.Page{ID: "p3", UserID: "u1", Visibility: model.PageVisibilityFollowers, LikedCount: 100}
	rows, err := svc.Featured("", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "p2", rows[0].ID)
	assert.Equal(t, "p1", rows[1].ID)
}

// --- Like ------------------------------------------------------------------

// TestHasLiked covers the lookup helper added for /api/pages/show
// (#1134). Empty / missing input は false で fail-soft (panic 無し)。
func TestHasLiked(t *testing.T) {
	svc, repo, likeRepo := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	assert.False(t, svc.HasLiked("u2", "p1"))
	require.NoError(t, likeRepo.Create(&model.PageLike{ID: "l1", UserID: "u2", PageID: "p1"}))
	assert.True(t, svc.HasLiked("u2", "p1"))
	assert.False(t, svc.HasLiked("", "p1"))
	assert.False(t, svc.HasLiked("u2", ""))
}

func TestLike_HappyPath(t *testing.T) {
	svc, repo, likeRepo := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	require.NoError(t, svc.Like("u2", "p1"))
	assert.Len(t, likeRepo.Likes, 1)
	assert.Equal(t, 1, repo.Pages["p1"].LikedCount)
}

func TestLike_UserRequired(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Like("", "p1")
	assert.Error(t, err)
}

func TestLike_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Like("u1", "missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestLike_PrivateAccessDenied(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityFollowers}
	err := svc.Like("u2", "p1")
	assert.ErrorIs(t, err, page.ErrAccessDenied)
}

func TestLike_OwnPrivateOK(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityFollowers}
	require.NoError(t, svc.Like("u1", "p1"))
}

func TestLike_AlreadyLiked(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	require.NoError(t, svc.Like("u2", "p1"))
	err := svc.Like("u2", "p1")
	assert.ErrorIs(t, err, page.ErrAlreadyLiked)
}

// failingExistsLikeRepo causes Exists to fail.
type failingExistsLikeRepo struct {
	*testutil.MockPageLikeRepository
}

func (r *failingExistsLikeRepo) Exists(_, _ string) (bool, error) {
	return false, errors.New("boom")
}

func TestLike_ExistsError(t *testing.T) {
	repo := testutil.NewMockPageRepository()
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	likeRepo := &failingExistsLikeRepo{MockPageLikeRepository: testutil.NewMockPageLikeRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := page.NewService(repo, likeRepo, idGen)
	err := svc.Like("u2", "p1")
	assert.Error(t, err)
}

// failingCreateLikeRepo causes Create to fail.
type failingCreateLikeRepo struct {
	*testutil.MockPageLikeRepository
}

func (r *failingCreateLikeRepo) Create(_ *model.PageLike) error { return errors.New("boom") }

func TestLike_CreateError(t *testing.T) {
	repo := testutil.NewMockPageRepository()
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	likeRepo := &failingCreateLikeRepo{MockPageLikeRepository: testutil.NewMockPageLikeRepository()}
	idGen, _ := id.NewGenerator("aidx")
	svc := page.NewService(repo, likeRepo, idGen)
	err := svc.Like("u2", "p1")
	assert.Error(t, err)
}

// --- Unlike ----------------------------------------------------------------

func TestUnlike_HappyPath(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	require.NoError(t, svc.Like("u2", "p1"))
	require.NoError(t, svc.Unlike("u2", "p1"))
	assert.Equal(t, 0, repo.Pages["p1"].LikedCount)
}

func TestUnlike_UserRequired(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Unlike("", "p1")
	assert.Error(t, err)
}

func TestUnlike_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	err := svc.Unlike("u1", "missing")
	assert.ErrorIs(t, err, page.ErrPageNotFound)
}

func TestUnlike_NotLiked(t *testing.T) {
	svc, repo, _ := newSvc(t)
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	err := svc.Unlike("u2", "p1")
	assert.ErrorIs(t, err, page.ErrNotLiked)
}

// failingDeleteLikeRepo causes Delete to fail.
type failingDeleteLikeRepo struct {
	*testutil.MockPageLikeRepository
}

func (r *failingDeleteLikeRepo) Delete(_ *model.PageLike) error { return errors.New("boom") }

func TestUnlike_DeleteError(t *testing.T) {
	repo := testutil.NewMockPageRepository()
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "u1", Visibility: model.PageVisibilityPublic}
	mock := testutil.NewMockPageLikeRepository()
	mock.Likes["pl1"] = &model.PageLike{ID: "pl1", UserID: "u2", PageID: "p1"}
	likeRepo := &failingDeleteLikeRepo{MockPageLikeRepository: mock}
	idGen, _ := id.NewGenerator("aidx")
	svc := page.NewService(repo, likeRepo, idGen)
	err := svc.Unlike("u2", "p1")
	assert.Error(t, err)
}

// --- SetClock --------------------------------------------------------------

func TestSetClock(t *testing.T) {
	svc, _, _ := newSvc(t)
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	svc.SetClock(nil)
	p, err := svc.Create(page.CreateInput{OwnerID: "u1", Title: "t", Name: "alpha"})
	require.NoError(t, err)
	assert.NotEmpty(t, p.ID)
}
