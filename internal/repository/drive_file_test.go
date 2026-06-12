package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func cleanupDriveFile(t *testing.T, id string) {
	t.Helper()
	testDB.Exec(`DELETE FROM "drive_file" WHERE id = ?`, id)
}

func newTestDriveFile(id, userID, md5 string, folderID *string) *model.DriveFile {
	uid := userID
	return &model.DriveFile{
		ID:             id,
		UserID:         &uid,
		MD5:            md5,
		Name:           "test.bin",
		Type:           "application/octet-stream",
		Size:           10,
		StoredInternal: true,
		URL:            "http://example.com/files/" + id,
		Properties:     datatypes.JSON([]byte("{}")),
		RequestHeaders: datatypes.JSON([]byte("{}")),
		FolderID:       folderID,
	}
}

func TestDriveFileRepository_CRUD(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_1", "dfu1")
	defer cleanupUser(t, user.ID)

	f := newTestDriveFile("f1", user.ID, "abc123", nil)
	require.NoError(t, repo.Create(f))
	defer cleanupDriveFile(t, f.ID)

	got, err := repo.FindByID("f1")
	require.NoError(t, err)
	assert.Equal(t, "test.bin", got.Name)

	require.NoError(t, repo.Update("f1", map[string]any{"name": "renamed.bin"}))
	got, _ = repo.FindByID("f1")
	assert.Equal(t, "renamed.bin", got.Name)

	require.NoError(t, repo.Update("f1", map[string]any{}))

	require.NoError(t, repo.Delete(f))
	_, err = repo.FindByID("f1")
	assert.Error(t, err)
}

func TestDriveFileRepository_FindByMD5(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_2", "dfu2")
	defer cleanupUser(t, user.ID)

	f1 := newTestDriveFile("f_md5_1", user.ID, "hash", nil)
	f2 := newTestDriveFile("f_md5_2", user.ID, "hash", nil)
	require.NoError(t, repo.Create(f1))
	require.NoError(t, repo.Create(f2))
	defer cleanupDriveFile(t, f1.ID)
	defer cleanupDriveFile(t, f2.ID)

	got, err := repo.FindByMD5(user.ID, "hash")
	require.NoError(t, err)
	// 最新 (id降順) を返す
	assert.Equal(t, "f_md5_2", got.ID)
}

func TestDriveFileRepository_ListByUser(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	folderRepo := NewDriveFolderRepository(testDB)
	user := insertTestUser(t, "u_df_3", "dfu3")
	defer cleanupUser(t, user.ID)

	uid := user.ID
	folder := &model.DriveFolder{ID: "fold_1", Name: "F", UserID: &uid}
	require.NoError(t, folderRepo.Create(folder))
	defer testDB.Exec(`DELETE FROM "drive_folder" WHERE id = ?`, folder.ID)

	folderID := "fold_1"
	root := newTestDriveFile("f_lst_1", user.ID, "h1", nil)
	infolder := newTestDriveFile("f_lst_2", user.ID, "h2", &folderID)
	require.NoError(t, repo.Create(root))
	require.NoError(t, repo.Create(infolder))
	defer cleanupDriveFile(t, root.ID)
	defer cleanupDriveFile(t, infolder.ID)

	rows, err := repo.ListByUser(user.ID, nil, false, "", "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "f_lst_1", rows[0].ID)

	rows, err = repo.ListByUser(user.ID, &folderID, false, "", "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "f_lst_2", rows[0].ID)

	// untilID/sinceIDの分岐を踏む (root のみ)
	rows, err = repo.ListByUser(user.ID, nil, false, "", "", "z", "", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	rows, err = repo.ListByUser(user.ID, nil, false, "", "", "", "a", 10)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestDriveFileRepository_ListByUser_TypeSortAnyFolder(t *testing.T) {
	// #1564: type filter (完全一致 / "/*" prefix)、sort、anyFolder の分岐。
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_ts", "dfts")
	defer cleanupUser(t, user.ID)

	folderRepo := NewDriveFolderRepository(testDB)
	uid := user.ID
	folder := &model.DriveFolder{ID: "fold_ts", Name: "F", UserID: &uid}
	require.NoError(t, folderRepo.Create(folder))
	defer testDB.Exec(`DELETE FROM "drive_folder" WHERE id = ?`, folder.ID)
	folderID := folder.ID

	png := newTestDriveFile("f_ts_1", user.ID, "t1", nil)
	png.Type = "image/png"
	png.Name = "bbb.png"
	png.Size = 10
	jpg := newTestDriveFile("f_ts_2", user.ID, "t2", &folderID)
	jpg.Type = "image/jpeg"
	jpg.Name = "aaa.jpg"
	jpg.Size = 30
	mp4 := newTestDriveFile("f_ts_3", user.ID, "t3", nil)
	mp4.Type = "video/mp4"
	mp4.Name = "ccc.mp4"
	mp4.Size = 20
	for _, f := range []*model.DriveFile{png, jpg, mp4} {
		require.NoError(t, repo.Create(f))
		defer cleanupDriveFile(t, f.ID)
	}

	// anyFolder=true は folder 条件なし (サブフォルダの jpg も入る)
	rows, err := repo.ListByUser(user.ID, nil, true, "", "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	// type prefix ("image/*") + anyFolder
	rows, err = repo.ListByUser(user.ID, nil, true, "image/*", "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// type 完全一致
	rows, err = repo.ListByUser(user.ID, nil, true, "video/mp4", "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, mp4.ID, rows[0].ID)

	// sort 各種 (anyFolder で全件対象)
	for _, tc := range []struct {
		sort  string
		first string
	}{
		{"+createdAt", mp4.ID}, // id DESC
		{"-createdAt", png.ID}, // id ASC
		{"+name", mp4.ID},      // name DESC (ccc)
		{"-name", jpg.ID},      // name ASC (aaa)
		{"+size", jpg.ID},      // size DESC (30)
		{"-size", png.ID},      // size ASC (10)
	} {
		rows, err = repo.ListByUser(user.ID, nil, true, "", tc.sort, "", "", 10)
		require.NoError(t, err, tc.sort)
		require.Len(t, rows, 3, tc.sort)
		assert.Equal(t, tc.first, rows[0].ID, tc.sort)
	}
}

func TestDriveFileRepository_FindAllByMD5(t *testing.T) {
	// #1564: find-by-hash の一致全件 (id ASC)。
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_am", "dfam")
	defer cleanupUser(t, user.ID)

	f1 := newTestDriveFile("f_am_1", user.ID, "samehash", nil)
	f2 := newTestDriveFile("f_am_2", user.ID, "samehash", nil)
	f3 := newTestDriveFile("f_am_3", user.ID, "otherhash", nil)
	for _, f := range []*model.DriveFile{f1, f2, f3} {
		require.NoError(t, repo.Create(f))
		defer cleanupDriveFile(t, f.ID)
	}

	got, err := repo.FindAllByMD5(user.ID, "samehash")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "f_am_1", got[0].ID)
	assert.Equal(t, "f_am_2", got[1].ID)

	got, err = repo.FindAllByMD5(user.ID, "ghost")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDriveFileRepository_FindByAnyURL(t *testing.T) {
	// #1564: drive/files/show の url anyOf 検索 (url/webpublicUrl/thumbnailUrl)。
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_au", "dfau")
	defer cleanupUser(t, user.ID)

	web := "http://example.com/files/f_au_web"
	thumb := "http://example.com/files/f_au_thumb"
	f := newTestDriveFile("f_au", user.ID, "aumd5", nil)
	f.WebpublicURL = &web
	f.ThumbnailURL = &thumb
	require.NoError(t, repo.Create(f))
	defer cleanupDriveFile(t, f.ID)

	for _, u := range []string{f.URL, web, thumb} {
		got, err := repo.FindByAnyURL(u)
		require.NoError(t, err, u)
		assert.Equal(t, f.ID, got.ID)
	}
	_, err := repo.FindByAnyURL("")
	assert.Error(t, err)
	_, err = repo.FindByAnyURL("http://example.com/none")
	assert.Error(t, err)
}

// TestDriveFileRepository_URLIndexes verifies that the #1625 url-column
// indexes (migration 000059-000061) are created and actually usable for the
// FindByAnyURL OR-equality query. testutil.ApplyMigrations は migration の
// エラーを swallow するため、これらが落ちても CI は沈黙し drive_file 肥大に
// 比例して静かに性能退行する。000054/000055 の GIN index guard
// (TestNoteRepository_MentionsFileIdsGINIndexes) と同じ regression guard。
func TestDriveFileRepository_URLIndexes(t *testing.T) {
	// 1. index が存在すること (CONCURRENTLY migration が適用されたことの確認
	//    も兼ねる)。nullable 列の 2 本は部分 index (WHERE IS NOT NULL) で
	//    あることも固定する。
	for _, tc := range []struct {
		index   string
		partial bool
	}{
		{"IDX_drive_file_url", false},
		{"IDX_drive_file_webpublicUrl", true},
		{"IDX_drive_file_thumbnailUrl", true},
	} {
		var indexdef string
		err := testDB.Raw(
			`SELECT indexdef FROM pg_indexes WHERE tablename = 'drive_file' AND indexname = ?`, tc.index,
		).Scan(&indexdef).Error
		require.NoError(t, err)
		require.NotEmpty(t, indexdef, "index %s must exist (migration applied)", tc.index)
		if tc.partial {
			assert.Contains(t, indexdef, "IS NOT NULL", "%s must be a partial index", tc.index)
		}
	}

	// 2. planner が各列の equality に index を選べること。小さな test テーブル
	//    では planner が seq scan を選ぶため、同一接続上で enable_seqscan=off
	//    を効かせて index 経路を強制し、plan に index 名が現れることを確認する
	//    (index が無ければ強制しても seq scan のまま)。SET LOCAL は transaction
	//    スコープなので Begin/Rollback で閉じる。
	checkPlanUsesIndex := func(query, arg, indexName string) {
		tx := testDB.Begin()
		defer tx.Rollback()
		require.NoError(t, tx.Exec(`SET LOCAL enable_seqscan = off`).Error)
		var lines []string
		require.NoError(t, tx.Raw(`EXPLAIN `+query, arg).Scan(&lines).Error)
		plan := strings.Join(lines, "\n")
		assert.Contains(t, plan, indexName,
			"equality query should use %s under enable_seqscan=off; plan was:\n%s", indexName, plan)
	}
	checkPlanUsesIndex(
		`SELECT * FROM "drive_file" WHERE "url" = ?`, "http://example.com/x", "IDX_drive_file_url")
	checkPlanUsesIndex(
		`SELECT * FROM "drive_file" WHERE "webpublicUrl" = ?`, "http://example.com/x", "IDX_drive_file_webpublicUrl")
	checkPlanUsesIndex(
		`SELECT * FROM "drive_file" WHERE "thumbnailUrl" = ?`, "http://example.com/x", "IDX_drive_file_thumbnailUrl")
}

func TestDriveFileRepository_FindByAnyAccessKey(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_df_ak", "dfak")
	defer cleanupUser(t, user.ID)

	primary := "pk-" + user.ID
	thumb := "tk-" + user.ID
	web := "wk-" + user.ID
	f := newTestDriveFile("f_ak", user.ID, "akmd5", nil)
	f.AccessKey = &primary
	f.ThumbnailAccessKey = &thumb
	f.WebpublicAccessKey = &web
	require.NoError(t, repo.Create(f))
	defer cleanupDriveFile(t, f.ID)

	// primary / thumbnail / webpublic いずれの key でも同じ行に解決する
	for _, k := range []string{primary, thumb, web} {
		got, err := repo.FindByAnyAccessKey(k)
		require.NoError(t, err, "lookup %s", k)
		assert.Equal(t, f.ID, got.ID)
	}

	// 空文字 / 未マッチは ErrRecordNotFound
	_, err := repo.FindByAnyAccessKey("")
	assert.Error(t, err)
	_, err = repo.FindByAnyAccessKey("does-not-exist")
	assert.Error(t, err)
}

func TestDriveFileRepository_QueryErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewDriveFileRepository(testDB.WithContext(ctx))

	err := repo.Create(&model.DriveFile{ID: "x"})
	assert.Error(t, err)
	_, err = repo.FindByID("a")
	assert.Error(t, err)
	_, err = repo.FindByMD5("a", "b")
	assert.Error(t, err)
	err = repo.Update("a", map[string]any{"name": "b"})
	assert.Error(t, err)
	_, err = repo.ListByUser("a", nil, false, "", "", "", "", 10)
	assert.Error(t, err)
}

func TestDriveFileRepository_ListForAdmin(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_admin_list", "adlf")
	defer cleanupUser(t, user.ID)

	localFile := newTestDriveFile("adl_local", user.ID, "md5local", nil)
	remoteHost := "remote.example"
	remoteFile := newTestDriveFile("adl_remote", user.ID, "md5remote", nil)
	remoteFile.UserHost = &remoteHost
	remoteFile.Type = "image/png"
	videoFile := newTestDriveFile("adl_video", user.ID, "md5video", nil)
	videoFile.Type = "video/mp4"
	require.NoError(t, repo.Create(localFile))
	require.NoError(t, repo.Create(remoteFile))
	require.NoError(t, repo.Create(videoFile))
	defer cleanupDriveFile(t, localFile.ID)
	defer cleanupDriveFile(t, remoteFile.ID)
	defer cleanupDriveFile(t, videoFile.ID)

	// origin = remote → only the remote-host file
	rows, err := repo.ListForAdmin("", "remote", "", "", "", "", 10)
	require.NoError(t, err)
	ids := make(map[string]bool)
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[remoteFile.ID])
	assert.False(t, ids[localFile.ID])

	// type = image/ prefix
	rows, err = repo.ListForAdmin("", "", "", "image/", "", "", 10)
	require.NoError(t, err)
	ids = make(map[string]bool)
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[remoteFile.ID])
	assert.False(t, ids[videoFile.ID])

	// host exact match
	rows, err = repo.ListForAdmin("", "", remoteHost, "", "", "", 10)
	require.NoError(t, err)
	found := false
	for _, r := range rows {
		if r.ID == remoteFile.ID {
			found = true
		}
	}
	assert.True(t, found)

	// userId narrows to that user's files only and ignores origin/host (#471)
	otherUser := insertTestUser(t, "u_adm_lst2", "adlf2")
	defer cleanupUser(t, otherUser.ID)
	otherFile := newTestDriveFile("adl_other", otherUser.ID, "md5other", nil)
	require.NoError(t, repo.Create(otherFile))
	defer cleanupDriveFile(t, otherFile.ID)
	rows, err = repo.ListForAdmin(user.ID, "remote", "", "", "", "", 10)
	require.NoError(t, err)
	for _, r := range rows {
		require.NotNil(t, r.UserID)
		assert.Equal(t, user.ID, *r.UserID)
	}

	// default limit / clamp
	_, err = repo.ListForAdmin("", "", "", "", "", "", 0)
	require.NoError(t, err)
	_, err = repo.ListForAdmin("", "", "", "", "", "", 1000)
	require.NoError(t, err)
}

func TestDriveFileRepository_ListSystemFiles(t *testing.T) {
	// #686: UserID IS NULL の system file (custom emoji copy / import zip 経路で
	// 蓄積される) を一覧する経路。type prefix と pagination が効くこと、
	// user 所有 / remote 所有のファイルが混ざらないことを保証する。
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_sys_lst", "syslst")
	defer cleanupUser(t, user.ID)

	// system file 2 件 (image/png + application/zip)、user 所有 1 件、remote 所有 1 件
	sysImg := newTestDriveFile("sys_img1", user.ID, "md5sysimg", nil)
	sysImg.UserID = nil
	sysImg.Type = "image/png"
	sysZip := newTestDriveFile("sys_zip1", user.ID, "md5syszip", nil)
	sysZip.UserID = nil
	sysZip.Type = "application/zip"
	userFile := newTestDriveFile("u_sys_uf", user.ID, "md5user", nil)
	userFile.Type = "image/png"
	remoteHost := "remote.example"
	remoteFile := newTestDriveFile("u_sys_rf", user.ID, "md5remote", nil)
	remoteFile.UserHost = &remoteHost
	remoteFile.Type = "image/png"
	require.NoError(t, repo.Create(sysImg))
	require.NoError(t, repo.Create(sysZip))
	require.NoError(t, repo.Create(userFile))
	require.NoError(t, repo.Create(remoteFile))
	defer cleanupDriveFile(t, sysImg.ID)
	defer cleanupDriveFile(t, sysZip.ID)
	defer cleanupDriveFile(t, userFile.ID)
	defer cleanupDriveFile(t, remoteFile.ID)

	// type 無指定: system file 2 件のみ
	rows, err := repo.ListSystemFiles("", "", "", 10)
	require.NoError(t, err)
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[sysImg.ID])
	assert.True(t, ids[sysZip.ID])
	assert.False(t, ids[userFile.ID], "user-owned file must not appear")
	assert.False(t, ids[remoteFile.ID], "remote-owned file must not appear")

	// type=image/ で絞り込み: 本 test の fixture では sysImg のみ image/* に該当。
	// 他 test が UserID=NULL + Type=image/* な system file 行を残しても本 test を
	// 巻き込まないよう、 fixture ID で scoped assert する (#1098)。
	rows, err = repo.ListSystemFiles("image/", "", "", 10)
	require.NoError(t, err)
	ids = make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.True(t, ids[sysImg.ID], "sysImg (image/png) should appear in image/ filter")
	assert.False(t, ids[sysZip.ID], "sysZip (application/zip) must not appear")
	assert.False(t, ids[userFile.ID], "user-owned file must not appear")
	assert.False(t, ids[remoteFile.ID], "remote-owned file must not appear")

	// limit 範囲外 (0 / 過大) でも error にならず default / clamp が効く
	_, err = repo.ListSystemFiles("", "", "", 0)
	require.NoError(t, err)
	_, err = repo.ListSystemFiles("", "", "", 1000)
	require.NoError(t, err)

	// pagination untilId: id < untilId (exclusive boundary)。sysZip より前
	// なら sysImg が出て sysZip は出ない。
	rows, err = repo.ListSystemFiles("", sysZip.ID, "", 10)
	require.NoError(t, err)
	gotIDs := make(map[string]bool, len(rows))
	for _, r := range rows {
		gotIDs[r.ID] = true
	}
	assert.True(t, gotIDs[sysImg.ID], "sysImg has smaller ID, should be included")
	assert.False(t, gotIDs[sysZip.ID], "sysZip is the cutoff and should be excluded")

	// pagination sinceId: id > sinceId (exclusive boundary)。sysImg より後
	// なら sysZip が出て sysImg は出ない。境界の semantics と sort 方向が
	// untilId 経路と対称であることを保証する。
	rows, err = repo.ListSystemFiles("", "", sysImg.ID, 10)
	require.NoError(t, err)
	gotIDs = make(map[string]bool, len(rows))
	for _, r := range rows {
		gotIDs[r.ID] = true
	}
	assert.True(t, gotIDs[sysZip.ID], "sysZip has larger ID, should be included")
	assert.False(t, gotIDs[sysImg.ID], "sysImg is the cutoff and should be excluded")
}

func TestDriveFileRepository_DeleteOrphansAndRemoteCache(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_del_src", "dels")
	defer cleanupUser(t, user.ID)

	// 孤立ファイル (userId NULL) と userId ありファイル
	orphan := newTestDriveFile("orph1", user.ID, "md5o", nil)
	orphan.UserID = nil
	kept := newTestDriveFile("kept1", user.ID, "md5k", nil)

	// cached remote file (isLink=false, userHost set) は DeleteRemoteCache の
	// 対象。link-only proxy (isLink=true) は対象外で保持される (upstream 互換)。
	host := "cache.example"
	remoteCache := newTestDriveFile("rc1", user.ID, "md5rc", nil)
	remoteCache.IsLink = false
	remoteCache.UserHost = &host
	linkOnly := newTestDriveFile("lo1", user.ID, "md5lo", nil)
	linkOnly.IsLink = true
	linkOnly.UserHost = &host

	require.NoError(t, repo.Create(orphan))
	require.NoError(t, repo.Create(kept))
	require.NoError(t, repo.Create(remoteCache))
	require.NoError(t, repo.Create(linkOnly))
	defer cleanupDriveFile(t, orphan.ID)
	defer cleanupDriveFile(t, kept.ID)
	defer cleanupDriveFile(t, remoteCache.ID)
	defer cleanupDriveFile(t, linkOnly.ID)

	n, err := repo.DeleteOrphans()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	_, err = repo.FindByID(orphan.ID)
	assert.Error(t, err)
	_, err = repo.FindByID(kept.ID)
	assert.NoError(t, err)

	n, err = repo.DeleteRemoteCache()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	_, err = repo.FindByID(remoteCache.ID)
	assert.Error(t, err, "isLink=false のキャッシュ実体は削除される")
	_, err = repo.FindByID(linkOnly.ID)
	assert.NoError(t, err, "isLink=true の link-only は保持される")
}

// TestDriveFileRepository_DeleteOrphans_PreservesEmojiReferenced は #722
// regression guard: emoji.originalUrl / publicUrl が参照している system 所有
// drive_file は cleanup で巻き込まれずに保持されること。#670 で導入された
// emoji copy / import zip の保管先が cleanup 実行で吹き飛ぶ即死バグの再発
// 防止。
func TestDriveFileRepository_DeleteOrphans_PreservesEmojiReferenced(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	emojiRepo := NewEmojiRepository(testDB)
	user := insertTestUser(t, "u_orph_emoji", "orphemo")
	defer cleanupUser(t, user.ID)

	// orphan true (emoji 参照無し): 削除されるべき
	pureOrphan := newTestDriveFile("orph_pure_722", user.ID, "md5o", nil)
	pureOrphan.UserID = nil
	pureOrphan.URL = "http://test/orph_pure_722.bin"

	// emoji.originalUrl 参照あり (system file): 削除してはいけない
	emojiOriginalRef := newTestDriveFile("emoji_orig_722", user.ID, "md5eo", nil)
	emojiOriginalRef.UserID = nil
	emojiOriginalRef.URL = "http://test/emoji_orig_722.png"

	// emoji.publicUrl 参照あり (webpublic 経由系): 削除してはいけない
	emojiPublicRef := newTestDriveFile("emoji_pub_722", user.ID, "md5ep", nil)
	emojiPublicRef.UserID = nil
	emojiPublicRef.URL = "http://test/emoji_pub_722.png"

	require.NoError(t, repo.Create(pureOrphan))
	require.NoError(t, repo.Create(emojiOriginalRef))
	require.NoError(t, repo.Create(emojiPublicRef))
	defer cleanupDriveFile(t, pureOrphan.ID)
	defer cleanupDriveFile(t, emojiOriginalRef.ID)
	defer cleanupDriveFile(t, emojiPublicRef.ID)

	// 2 件 emoji 行を seed (1 件は originalUrl で参照、もう 1 件は publicUrl で参照)
	emoOrig := &model.Emoji{
		ID:          "e_orig_722",
		Name:        "guard_emoji_orig_722",
		OriginalURL: emojiOriginalRef.URL,
		PublicURL:   emojiOriginalRef.URL,
	}
	emoPub := &model.Emoji{
		ID:          "e_pub_722",
		Name:        "guard_emoji_pub_722",
		OriginalURL: "http://test/some_other_url.png", // originalUrl は別
		PublicURL:   emojiPublicRef.URL,               // publicUrl で参照
	}
	require.NoError(t, emojiRepo.Create(emoOrig))
	require.NoError(t, emojiRepo.Create(emoPub))
	defer testDB.Exec(`DELETE FROM "emoji" WHERE id IN (?, ?)`, emoOrig.ID, emoPub.ID)

	n, err := repo.DeleteOrphans()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1), "pureOrphan should be counted in deleted rows")

	// pureOrphan は消えている
	_, err = repo.FindByID(pureOrphan.ID)
	assert.Error(t, err, "pure orphan must be deleted")

	// emojiOriginalRef / emojiPublicRef は保持されている
	_, err = repo.FindByID(emojiOriginalRef.ID)
	assert.NoError(t, err, "emoji.originalUrl 参照の system file は保持される")
	_, err = repo.FindByID(emojiPublicRef.ID)
	assert.NoError(t, err, "emoji.publicUrl 参照の system file は保持される")
}

// --- 追加テスト (#260 repository coverage) ---

func TestDriveFileRepository_FindByIDs(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_ids_u1")
	f1 := newTestDriveFile("df_ids_1", "df_ids_u1", "m1", nil)
	f2 := newTestDriveFile("df_ids_2", "df_ids_u1", "m2", nil)
	require.NoError(t, repo.Create(f1))
	require.NoError(t, repo.Create(f2))
	t.Cleanup(func() { cleanupDriveFile(t, f1.ID); cleanupDriveFile(t, f2.ID) })

	files, err := repo.FindByIDs([]string{f1.ID, f2.ID, "missing"})
	require.NoError(t, err)
	assert.Len(t, files, 2)

	empty, err := repo.FindByIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestDriveFileRepository_FindByName(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_name_u1")
	f := newTestDriveFile("df_name_1", "df_name_u1", "mn", nil)
	f.Name = "uniq_name.png"
	require.NoError(t, repo.Create(f))
	t.Cleanup(func() { cleanupDriveFile(t, f.ID) })

	// folderID nil path (root)
	got, err := repo.FindByName("df_name_u1", "uniq_name.png", nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)

	// folderID non-nil path (should not match because no folder set)
	other := "folder_dummy"
	got, err = repo.FindByName("df_name_u1", "uniq_name.png", &other)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDriveFileRepository_ExistsByMD5(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_md5_u1")
	f := newTestDriveFile("df_md5_1", "df_md5_u1", "md5_unique", nil)
	require.NoError(t, repo.Create(f))
	t.Cleanup(func() { cleanupDriveFile(t, f.ID) })

	ok, err := repo.ExistsByMD5("df_md5_u1", "md5_unique")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.ExistsByMD5("df_md5_u1", "md5_nonexistent")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestDriveFileRepository_ListByFileIDs(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_lbf_u1")
	f1 := newTestDriveFile("df_lbf_1", "df_lbf_u1", "lbf1", nil)
	require.NoError(t, repo.Create(f1))
	t.Cleanup(func() { cleanupDriveFile(t, f1.ID) })

	got, err := repo.ListByFileIDs([]string{f1.ID})
	require.NoError(t, err)
	assert.Len(t, got, 1)

	empty, err := repo.ListByFileIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestDriveFileRepository_UsageByUser(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_usg_u1")
	f := newTestDriveFile("df_usg_1", "df_usg_u1", "usg", nil)
	f.Size = 1234
	require.NoError(t, repo.Create(f))
	t.Cleanup(func() { cleanupDriveFile(t, f.ID) })

	total, err := repo.UsageByUser("df_usg_u1")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, int64(1234))

	// 存在しないユーザー
	total, err = repo.UsageByUser("df_usg_none")
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

func TestDriveFileRepository_UpdateBulkFolder(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_bulk_u1")
	// folderを作成
	require.NoError(t, testDB.Exec(
		`INSERT INTO "drive_folder" (id, name, "userId") VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		"df_bulk_folder", "folder_x", "df_bulk_u1",
	).Error)
	f := newTestDriveFile("df_bulk_1", "df_bulk_u1", "bk", nil)
	require.NoError(t, repo.Create(f))
	t.Cleanup(func() {
		cleanupDriveFile(t, f.ID)
		testDB.Exec(`DELETE FROM "drive_folder" WHERE id = ?`, "df_bulk_folder")
	})

	target := "df_bulk_folder"
	require.NoError(t, repo.UpdateBulkFolder("df_bulk_u1", []string{f.ID}, &target))

	got, err := repo.FindByID(f.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FolderID)
	assert.Equal(t, "df_bulk_folder", *got.FolderID)

	// IDOR guard: 別ユーザーIDで同じ fileId を移動しても、所有者が違うため
	// 行は更新されない (folderId が維持される)。
	other := "df_bulk_other_folder"
	require.NoError(t, repo.UpdateBulkFolder("someone_else", []string{f.ID}, &other))
	got, err = repo.FindByID(f.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FolderID)
	assert.Equal(t, "df_bulk_folder", *got.FolderID, "other user must not move the file (IDOR)")
}

func TestDriveFileRepository_DeleteByUser(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	seedUser(t, "df_del_u1")
	f := newTestDriveFile("df_del_1", "df_del_u1", "dl", nil)
	require.NoError(t, repo.Create(f))

	// 空userIDはno-op
	n, err := repo.DeleteByUser("")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// 該当ユーザー分が削除される
	n, err = repo.DeleteByUser("df_del_u1")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	_, err = repo.FindByID(f.ID)
	assert.Error(t, err)
}

var _ = context.Background // import guard

// ListRemoteCache / ListByUserAll / DeleteByIDs の動作 (#H-PR8f)。
func TestDriveFileRepository_CleanupHelpers(t *testing.T) {
	repo := NewDriveFileRepository(testDB)
	user := insertTestUser(t, "u_clh", "clh")
	defer cleanupUser(t, user.ID)
	host := "clh.example"

	cached := newTestDriveFile("clh_c", user.ID, "md5c", nil)
	cached.IsLink = false
	cached.UserHost = &host
	link := newTestDriveFile("clh_l", user.ID, "md5l", nil)
	link.IsLink = true
	link.UserHost = &host
	local := newTestDriveFile("clh_loc", user.ID, "md5loc", nil)
	for _, f := range []*model.DriveFile{cached, link, local} {
		require.NoError(t, repo.Create(f))
		defer cleanupDriveFile(t, f.ID)
	}

	idset := func(fs []*model.DriveFile) map[string]bool {
		m := map[string]bool{}
		for _, f := range fs {
			m[f.ID] = true
		}
		return m
	}

	// ListRemoteCache は isLink=false かつ host あり (= キャッシュ実体) のみ。
	rc, err := repo.ListRemoteCache(100)
	require.NoError(t, err)
	rcIDs := idset(rc)
	assert.True(t, rcIDs[cached.ID], "cached を含む")
	assert.False(t, rcIDs[link.ID], "link-only は含まない")
	assert.False(t, rcIDs[local.ID], "local は含まない")

	// ListByUserAll は user 所有の全 file (folder 問わず)。
	ua, err := repo.ListByUserAll(user.ID, 100)
	require.NoError(t, err)
	uaIDs := idset(ua)
	assert.True(t, uaIDs[cached.ID] && uaIDs[link.ID] && uaIDs[local.ID])

	// DeleteByIDs は指定 ID のみ削除。
	n, err := repo.DeleteByIDs([]string{cached.ID, link.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
	_, err = repo.FindByID(cached.ID)
	assert.Error(t, err)
	_, err = repo.FindByID(local.ID)
	assert.NoError(t, err, "指定外の local は残る")
}
