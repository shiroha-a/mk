package flash

import (
	"testing"

	"github.com/shiroha-a/mk/internal/misc/id"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/require"
)

// **非公開の Flash を所有者以外に返さないこと。**
//
// `flash/show` は `script` 全文を返す。`visibility` 列は実在し、
// `flash/create` / `update` が `private` を受け付けるのに、show だけが
// 誰にでも返していた。
func TestShow_HidesPrivateFlashFromOthers(t *testing.T) {
	t.Parallel()

	repo := testutil.NewMockFlashRepository()
	require.NoError(t, repo.Create(&model.Flash{
		ID: "f1", UserID: "owner", Visibility: "private", Script: "SECRET",
	}))
	idGen, _ := id.NewGenerator("aidx")
	svc := NewService(repo, nil, idGen)

	_, err := svc.Show("", "f1")
	require.ErrorIs(t, err, ErrFlashNotFound, "未認証には返さないこと")

	_, err = svc.Show("stranger", "f1")
	require.ErrorIs(t, err, ErrFlashNotFound, "他人には返さないこと")

	got, err := svc.Show("owner", "f1")
	require.NoError(t, err, "所有者には返すこと")
	require.Equal(t, "SECRET", got.Script)
}

// public は従来どおり誰でも読めること。
func TestShow_KeepsPublicFlashReadable(t *testing.T) {
	t.Parallel()

	repo := testutil.NewMockFlashRepository()
	require.NoError(t, repo.Create(&model.Flash{ID: "f2", UserID: "owner", Visibility: "public"}))
	idGen, _ := id.NewGenerator("aidx")
	svc := NewService(repo, nil, idGen)

	_, err := svc.Show("", "f2")
	require.NoError(t, err)

	// visibility 未設定 (既定) も同じ扱い。
	require.NoError(t, repo.Create(&model.Flash{ID: "f3", UserID: "owner"}))
	_, err = svc.Show("", "f3")
	require.NoError(t, err)
}
