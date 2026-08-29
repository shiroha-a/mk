package testutil

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
)

// mock の cursor ページングが production の paginationOrder と同じ意味であること
// (#2713)。
//
// **順序だけの問題ではない。** limit で打ち切ると残る行そのものが変わる
// (DESC 固定の mock は cursor 以降の最新 N 件、production は cursor 直後の
// 最古 N 件)。ここが無検証だと、handler テストが緑のまま production が別の
// ページを返す状態に戻せてしまう。
//
// mock 経由のケースは cursor の後ろに 5 件置き limit=2 で取る。1 件しか
// 置かないと ASC でも DESC でも同じ結果になり、向きを検出できない。
// 例外は 2 つ: ModerationLog は slice なので 3 件、ListMessagesByRoom は
// fixture の都合で 1 件 (向きは見ない。当該サブテストに理由を書いてある)。
// `TestSortMockPage_MatchesPaginationOrder` は helper を直接叩くので、
// cursor による絞り込みと limit 打ち切りを経ない。

func pageIDs[T any](rows []T, id func(T) string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}

func TestSortMockPage_MatchesPaginationOrder(t *testing.T) {
	type row struct{ id string }
	get := func(r row) string { return r.id }
	cases := []struct {
		name             string
		sinceID, untilID string
		want             []string
	}{
		{"since only flips to ASC", "a0", "", []string{"a1", "a2", "a3"}},
		{"until only stays DESC", "", "a9", []string{"a3", "a2", "a1"}},
		{"both stay DESC", "a0", "a9", []string{"a3", "a2", "a1"}},
		{"no cursor stays DESC", "", "", []string{"a3", "a2", "a1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := []row{{"a2"}, {"a1"}, {"a3"}}
			SortMockPage(rows, tc.sinceID, tc.untilID, get)
			assert.Equal(t, tc.want, pageIDs(rows, get))
		})
	}
}

func TestMockChatRepository_SinceOnlyIsAscending(t *testing.T) {
	m := NewMockChatRepository()
	// 候補は cursor の後ろに 5 件。ここで守れる強さは変異の種類で違う (実測):
	//
	//   - SortMockPage の向きを間違える → 50/50 で落ちる (決定的)
	//   - SortMockPage の呼び出しごと消す → 25/50。mock は map 走査で並びが
	//     非決定になるため確率的にしか落ちない
	//
	// **一様シャッフルではない** (Go の map は小さいうちは挿入順の相対順序が
	// かなり残る) ので、候補を増やしても順列の数ほどは下がらない。候補 3 件
	// から 5 件への増加で見逃しは 3/4 から 1/2 になった程度。
	for i := 0; i < 6; i++ {
		rid := fmt.Sprintf("r%d", i)
		m.Rooms[rid] = &model.ChatRoom{ID: rid, OwnerID: "owner"}
		m.Memberships[rid] = &model.ChatRoomMembership{ID: rid, RoomID: rid, UserID: "member"}
		m.Invitations[rid] = &model.ChatRoomInvitation{ID: rid, RoomID: "room", UserID: "invitee"}
		room := rid
		fileID := "file"
		m.Messages[rid] = &model.ChatMessage{ID: rid, FromUserID: "owner", ToRoomID: &room, FileID: &fileID}

		// DM は room 側と id 空間を分ける。ListMessagesByUser の向きは
		// room 経由のケースでは見られない (room ごとに 1 件しか無い)。
		dmID := fmt.Sprintf("dm%d", i)
		peer := "peer"
		m.Messages[dmID] = &model.ChatMessage{ID: dmID, FromUserID: "owner", ToUserID: &peer}
	}
	roomID := func(r *model.ChatRoom) string { return r.ID }
	memID := func(r *model.ChatRoomMembership) string { return r.ID }
	msgID := func(r *model.ChatMessage) string { return r.ID }

	t.Run("ListRoomsByOwner", func(t *testing.T) {
		rows, err := m.ListRoomsByOwner("owner", "r0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r1", "r2"}, pageIDs(rows, roomID))
	})
	t.Run("ListJoinedRooms", func(t *testing.T) {
		rows, err := m.ListJoinedRooms("member", "r0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r1", "r2"}, pageIDs(rows, roomID))
	})
	t.Run("ListMessagesByRoom", func(t *testing.T) {
		// fixture では 1 room = 1 message なので、ここで見えるのは cursor が
		// 効いていることだけ。**向きは ListMessagesByUser / ListMessagesByFileID
		// のサブテストが見る** (同じ SortMockPage 呼び出しではないので、
		// ByRoom 側の行が消えてもここは落ちない点に注意)。
		rows, err := m.ListMessagesByRoom("r2", "r1", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r2"}, pageIDs(rows, msgID))
	})
	t.Run("ListMessagesByUser", func(t *testing.T) {
		rows, err := m.ListMessagesByUser("owner", "peer", "dm0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"dm1", "dm2"}, pageIDs(rows, msgID))
	})
	t.Run("ListMessagesByFileID", func(t *testing.T) {
		rows, err := m.ListMessagesByFileID("file", "", "r0", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r1", "r2"}, pageIDs(rows, msgID))
	})
	t.Run("ListMembersByRoomPaged", func(t *testing.T) {
		for i := 0; i < 6; i++ {
			id := fmt.Sprintf("m%d", i)
			m.Memberships[id] = &model.ChatRoomMembership{ID: id, RoomID: "shared", UserID: "u"}
		}
		rows, err := m.ListMembersByRoomPaged("shared", "m0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"m1", "m2"}, pageIDs(rows, memID))
	})
	t.Run("ListMembershipsByUser", func(t *testing.T) {
		rows, err := m.ListMembershipsByUser("member", "r0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r1", "r2"}, pageIDs(rows, memID))
	})
	t.Run("ListInvitationsByRoom", func(t *testing.T) {
		rows, err := m.ListInvitationsByRoom("room", "r0", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"r1", "r2"}, pageIDs(rows, func(i *model.ChatRoomInvitation) string { return i.ID }))
	})
}

func TestMockDriveFolderRepository_SinceOnlyIsAscending(t *testing.T) {
	m := NewMockDriveFolderRepository()
	owner := "u"
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("d%d", i)
		m.Folders[id] = &model.DriveFolder{ID: id, UserID: &owner}
	}
	rows, err := m.ListByUser(owner, nil, "", "d0", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"d1", "d2"}, pageIDs(rows, func(f *model.DriveFolder) string { return f.ID }))
}

func TestMockAbuseReportRepository_SinceOnlyIsAscending(t *testing.T) {
	m := NewMockAbuseReportRepository()
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("ar%d", i)
		m.Reports[id] = &model.AbuseUserReport{ID: id, TargetUserID: "t", ReporterID: "r"}
	}
	rows, err := m.List(nil, "", "", "ar0", "", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"ar1", "ar2"}, pageIDs(rows, func(r *model.AbuseUserReport) string { return r.ID }))
}

func TestMockModerationLogRepository_SinceOnlyIsAscending(t *testing.T) {
	m := NewMockModerationLogRepository()
	// 挿入順を id 順とわざと逆にする。旧 mock は並べ替えずに limit を切って
	// いたので、この順でないと「ソートしていない」状態を検出できない。
	for _, id := range []string{"ml3", "ml2", "ml1", "ml0"} {
		m.Logs = append(m.Logs, &model.ModerationLog{ID: id, UserID: "u", Type: "suspend"})
	}
	rows, err := m.List(model.ModerationLogFilter{SinceID: "ml0", Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"ml1", "ml2"}, pageIDs(rows, func(l *model.ModerationLog) string { return l.ID }))
}

func TestMockRegistrationTicketRepository_SinceOnlyIsAscending(t *testing.T) {
	m := NewMockRegistrationTicketRepository()
	creator := "c"
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("t%d", i)
		m.Tickets[id] = &model.RegistrationTicket{ID: id, CreatedByID: &creator}
	}
	rows, err := m.ListByCreator(creator, "t0", "", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"t1", "t2"}, pageIDs(rows, func(t *model.RegistrationTicket) string { return t.ID }))
}

// mock の owner 無しリモート添付の候補抽出が production と同じ意味であること
// (#2722)。
//
// **この mock を使うテストがまだ無くても要る。** 添付の掃除は「表示中の添付を
// 消さない」ことが唯一の要件で、guard を 1 つでも落とすと即データ消失になる。
// mock 側が緩いと、後からこの mock でハンドラを書いた人が production では
// 起きない前提でテストを書ける。
func TestMockDriveFileRepository_OrphanRemoteAttachmentCandidates(t *testing.T) {
	owner := "u1"
	host := "remote.example"
	m := NewMockDriveFileRepository()
	add := func(id string, mutate func(*model.DriveFile)) {
		f := &model.DriveFile{ID: id, UserHost: &host, IsLink: true}
		if mutate != nil {
			mutate(f)
		}
		m.Files[id] = f
	}
	add("a_garbage", nil)
	add("b_referenced", nil)
	add("c_owned", func(f *model.DriveFile) { f.UserID = &owner })
	add("d_local", func(f *model.DriveFile) { f.UserHost = nil })
	add("e_cached", func(f *model.DriveFile) { f.IsLink = false })
	add("z_fresh", nil)
	m.NoteReferencedFileIDs = map[string]bool{"b_referenced": true}

	ids, err := m.ListOrphanRemoteAttachmentCandidates("z", "", 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"a_garbage"}, ids)

	t.Run("cursor and limit", func(t *testing.T) {
		for _, id := range []string{"a2", "a3"} {
			add(id, nil)
		}
		first, err := m.ListOrphanRemoteAttachmentCandidates("z", "", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"a2", "a3"}, first, "id 昇順で limit ぶん")

		next, err := m.ListOrphanRemoteAttachmentCandidates("z", "a3", 2)
		require.NoError(t, err)
		assert.Equal(t, []string{"a_garbage"}, next, "cursor の続きから")
	})

	t.Run("cutoff is exclusive", func(t *testing.T) {
		// production 側 (repository) も cutoff と同じ id は候補にしない。
		// mock だけ緩いと「境界の行は消えない」前提のテストが書ける。
		// 他の subtest が m.Files に足した行と混ざらないよう作り直す。
		only := NewMockDriveFileRepository()
		only.Files["k1"] = &model.DriveFile{ID: "k1", UserHost: &host, IsLink: true}
		ids, err := only.ListOrphanRemoteAttachmentCandidates("k1", "", 100)
		require.NoError(t, err)
		assert.Empty(t, ids, "cutoff と同じ id は含めない")

		ids, err = only.ListOrphanRemoteAttachmentCandidates("k2", "", 100)
		require.NoError(t, err)
		assert.Equal(t, []string{"k1"}, ids)
	})

	t.Run("empty cutoff is a no-op", func(t *testing.T) {
		ids, err := m.ListOrphanRemoteAttachmentCandidates("", "", 100)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})
}
