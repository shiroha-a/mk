package testutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// TestMockDriveFileRepository_MatchesProductionPredicates は #2747 で追加した、
// mock と production (internal/repository/drive_file.go) の選択条件のずれを
// 固定するテスト。
//
// #2744 と同じ型の drift が 2 系統残っていた。
//
//   - 述語そのものがずれていたもの: FindByAccessKey / FindByName (file) /
//     FindByName (folder) / ListForAdmin / ListSystemFiles
//   - 多重一致時の戻りが非決定だったもの: FindByAnyURL / FindByAnyAccessKey /
//     FindByURI (production の First() は ORDER BY id で最小 ID を返す)
//
// **既存テストが誤検証していたわけではない。** 修正前後で挙動が変わる
// fixture を持つ既存テストは fileType 経路の 2 件だけで (本 PR で
// `image/*` に直した)、access key の 2 メソッドはそもそも mock 経由で
// 呼ばれていなかった。残りは将来の誤用に対する予防。
func TestMockDriveFileRepository_MatchesProductionPredicates(t *testing.T) {
	ptr := func(s string) *string { return &s }

	t.Run("FindByAccessKey matches the primary key only", func(t *testing.T) {
		// production は `"accessKey" = ?` 単独。thumbnail / webpublic の OR は
		// 呼び出し側が primary 一致でしか swap しないため dead clause として
		// 削除済み (#637 review UR-014)。
		repo := NewMockDriveFileRepository()
		repo.Files["f1"] = &model.DriveFile{
			ID:                 "f1",
			AccessKey:          ptr("primary"),
			ThumbnailAccessKey: ptr("thumb"),
			WebpublicAccessKey: ptr("webpub"),
		}

		got, err := repo.FindByAccessKey("primary")
		require.NoError(t, err)
		assert.Equal(t, "f1", got.ID)

		_, err = repo.FindByAccessKey("thumb")
		assert.ErrorIs(t, err, ErrNotFound, "thumbnail key は primary 経路では引けない")
		_, err = repo.FindByAccessKey("webpub")
		assert.ErrorIs(t, err, ErrNotFound, "webpublic key は primary 経路では引けない")
	})

	t.Run("FindByAnyAccessKey matches all three columns", func(t *testing.T) {
		// production はこちらが 3 列 OR。2 経路の差を mock でも表現する (#1414)。
		repo := NewMockDriveFileRepository()
		repo.Files["f1"] = &model.DriveFile{
			ID:                 "f1",
			AccessKey:          ptr("primary"),
			ThumbnailAccessKey: ptr("thumb"),
			WebpublicAccessKey: ptr("webpub"),
		}

		for _, key := range []string{"primary", "thumb", "webpub"} {
			got, err := repo.FindByAnyAccessKey(key)
			require.NoError(t, err, "key=%s", key)
			assert.Equal(t, "f1", got.ID, "key=%s", key)
		}
		// 空文字ガードの検証には**空文字のキーを持つ fixture**が要る。
		// 非空キーだけだとガードを外してもループが何も掴まず ErrNotFound に
		// なり、テストが空振りする。
		blank := NewMockDriveFileRepository()
		blank.Files["blank"] = &model.DriveFile{ID: "blank", AccessKey: ptr(""), ThumbnailAccessKey: ptr("")}
		_, err := blank.FindByAnyAccessKey("")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("FindByName scopes to the folder", func(t *testing.T) {
		// production は folderID が nil なら `"folderId" IS NULL`。
		// 「どのフォルダでもよい」ではない。
		repo := NewMockDriveFileRepository()
		u := "u1"
		folder := "fld1"
		repo.Files["root"] = &model.DriveFile{ID: "root", UserID: &u, Name: "a.png"}
		repo.Files["infolder"] = &model.DriveFile{ID: "infolder", UserID: &u, Name: "a.png", FolderID: &folder}

		got, err := repo.FindByName(u, "a.png", nil)
		require.NoError(t, err)
		require.Len(t, got, 1, "folderID nil は root 直下のみ")
		assert.Equal(t, "root", got[0].ID)

		got, err = repo.FindByName(u, "a.png", &folder)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "infolder", got[0].ID)
	})

	t.Run("FindByAnyURL rejects the empty string", func(t *testing.T) {
		// production は空文字で即 ErrRecordNotFound。ガードが無いと URL 未設定の
		// fixture が map 反復順で任意に 1 件返る (テストが偶発的に通る/落ちる)。
		repo := NewMockDriveFileRepository()
		repo.Files["f1"] = &model.DriveFile{ID: "f1"}

		_, err := repo.FindByAnyURL("")
		assert.ErrorIs(t, err, ErrNotFound)
	})
}

// TestMatchesDriveFileType は fileType 述語が production と同じ形であることを
// 固定する (#1772)。
//
// 無条件 prefix だと `image/*` がどこにも当たらず (`image/*` で始まる MIME は
// 無い)、逆に完全一致のつもりの指定がより長いサブタイプまで拾う。
// どちらも本番と逆に振れる。
func TestMatchesDriveFileType(t *testing.T) {
	cases := []struct {
		name     string
		actual   string
		fileType string
		want     bool
	}{
		{"empty filter matches anything", "image/png", "", true},
		{"wildcard matches the prefix", "image/png", "image/*", true},
		{"wildcard rejects another prefix", "video/mp4", "image/*", false},
		{"exact matches", "image/png", "image/png", true},
		{"exact rejects a longer subtype", "image/heic-sequence", "image/heic", false},
		{"trailing slash is not a wildcard", "image/png", "image/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, matchesDriveFileType(tc.actual, tc.fileType))
		})
	}
}

// TestMockDriveFileRepository_ListFileTypeSemantics は上の述語が実際に
// ListForAdmin / ListSystemFiles / ListByUser の 3 経路で使われていることを
// 固定する。#2747 以前は前 2 者だけが無条件 prefix で取り残されていた。
func TestMockDriveFileRepository_ListFileTypeSemantics(t *testing.T) {
	u := "u1"
	newRepo := func() *MockDriveFileRepository {
		repo := NewMockDriveFileRepository()
		repo.Files["img"] = &model.DriveFile{ID: "img", UserID: &u, Type: "image/png"}
		repo.Files["vid"] = &model.DriveFile{ID: "vid", UserID: &u, Type: "video/mp4"}
		repo.Files["sys"] = &model.DriveFile{ID: "sys", Type: "image/webp"}
		return repo
	}

	t.Run("ListForAdmin", func(t *testing.T) {
		rows, err := newRepo().ListForAdmin(u, "", "", "image/*", "", "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "img", rows[0].ID)

		rows, err = newRepo().ListForAdmin(u, "", "", "image/", "", "", 10)
		require.NoError(t, err)
		assert.Empty(t, rows, "`/*` の無い指定は完全一致")
	})

	t.Run("ListSystemFiles", func(t *testing.T) {
		rows, err := newRepo().ListSystemFiles("image/*", "", "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "sys", rows[0].ID)

		rows, err = newRepo().ListSystemFiles("image/", "", "", 10)
		require.NoError(t, err)
		assert.Empty(t, rows, "`/*` の無い指定は完全一致")
	})

	t.Run("ListByUser", func(t *testing.T) {
		rows, err := newRepo().ListByUser(u, nil, true, "image/*", "", "", "", 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "img", rows[0].ID)

		rows, err = newRepo().ListByUser(u, nil, true, "image/", "", "", "", 10)
		require.NoError(t, err)
		assert.Empty(t, rows, "`/*` の無い指定は完全一致")
	})

	// anyFolder=false のとき folderID nil が root 直下に限定されること
	// (matchesDriveFolder を ListByUser 経由でも踏む)。
	t.Run("ListByUser scopes to the folder", func(t *testing.T) {
		repo := newRepo()
		folder := "fld1"
		repo.Files["infolder"] = &model.DriveFile{ID: "infolder", UserID: &u, Type: "image/png", FolderID: &folder}

		rows, err := repo.ListByUser(u, nil, false, "", "", "", "", 10)
		require.NoError(t, err)
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		assert.NotContains(t, ids, "infolder", "folderID nil は root 直下のみ")
	})
}

// TestMockDriveFolderRepository_FindByNameScopesToParent は #2747 のレビューで
// 見つかった、file 側と同型の drift を固定する。
//
// production (internal/repository/drive_folder.go) は parentID が nil なら
// `"parentId" IS NULL`。mock は第 3 引数を捨てていたので、root と配下に同名
// フォルダがあると本番 1 件・mock 2 件になっていた。
func TestMockDriveFolderRepository_FindByNameScopesToParent(t *testing.T) {
	repo := NewMockDriveFolderRepository()
	u := "u1"
	parent := "p1"
	repo.Folders["root"] = &model.DriveFolder{ID: "root", UserID: &u, Name: "docs"}
	repo.Folders["nested"] = &model.DriveFolder{ID: "nested", UserID: &u, Name: "docs", ParentID: &parent}

	got, err := repo.FindByName(u, "docs", nil)
	require.NoError(t, err)
	require.Len(t, got, 1, "parentID nil は root 直下のみ")
	assert.Equal(t, "root", got[0].ID)

	got, err = repo.FindByName(u, "docs", &parent)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "nested", got[0].ID)
}

// TestMockDriveFileRepository_MultiMatchIsDeterministic は多重一致時の戻りが
// production と同じく最小 ID になることを固定する。
//
// production の `First()` は `ORDER BY id` を付けるので決定的だが、mock は
// map を回すので反復順に依存していた。同 fixture で実行ごとに別のファイルを
// 返すと、所有者判定の対象が揺れる。FindByMD5 は既に決定化してある。
func TestMockDriveFileRepository_MultiMatchIsDeterministic(t *testing.T) {
	ptr := func(s string) *string { return &s }
	const shared = "https://media.example/x.webp"

	t.Run("FindByAnyURL", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			repo := NewMockDriveFileRepository()
			repo.Files["b"] = &model.DriveFile{ID: "b", URL: shared}
			repo.Files["a"] = &model.DriveFile{ID: "a", ThumbnailURL: ptr(shared)}

			got, err := repo.FindByAnyURL(shared)
			require.NoError(t, err)
			require.Equal(t, "a", got.ID, "最小 ID を返す (production の ORDER BY id)")
		}
	})

	// uri の index は非 unique (migration/000040) なので多重一致は production
	// でも起きる。呼び出し回数は mock 経由で最多 (federation の dedup 経路)。
	t.Run("FindByURI", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			repo := NewMockDriveFileRepository()
			repo.Files["b"] = &model.DriveFile{ID: "b", URI: ptr("https://remote/o/1")}
			repo.Files["a"] = &model.DriveFile{ID: "a", URI: ptr("https://remote/o/1")}

			got, err := repo.FindByURI("https://remote/o/1")
			require.NoError(t, err)
			require.Equal(t, "a", got.ID, "最小 ID を返す (production の ORDER BY id)")
		}
	})

	t.Run("FindByAnyAccessKey", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			repo := NewMockDriveFileRepository()
			repo.Files["b"] = &model.DriveFile{ID: "b", AccessKey: ptr("k")}
			repo.Files["a"] = &model.DriveFile{ID: "a", ThumbnailAccessKey: ptr("k")}

			got, err := repo.FindByAnyAccessKey("k")
			require.NoError(t, err)
			require.Equal(t, "a", got.ID, "最小 ID を返す (production の ORDER BY id)")
		}
	})
}
