package transfer

import (
	"bytes"
	"fmt"
	"strconv"
	"time"
)

// csvBatchSize controls how many rows are fetched per pagination step for
// CSV exports (follow/block/mute/user-lists). 500 matches upstream.
const csvBatchSize = 500

// inactiveThreshold is upstream の excludeInactive 判定閾値 (90日)。
// updatedAt がこれより古い followee は inactive とみなして除外する。
const inactiveThreshold = 90 * 24 * time.Hour

// exportFollowing emits one `acct,withReplies=(true|false)` line per followee.
// 出力順は登録降順 (id DESC)。excludeMuting=true なら muted user を、
// excludeInactive=true なら 90日以上 updatedAt が古い followee を除外する
// (upstream export-following.ts + ExportFollowingProcessorService)。
func (e *Exporter) exportFollowing(userID string, excludeMuting, excludeInactive bool) ([]byte, error) {
	// excludeMuting 用の muteeId 集合を 1 度だけ取得する。
	var muted map[string]struct{}
	if excludeMuting {
		ids, err := e.deps.MutingRepo.ListMuteeIDs(userID)
		if err != nil {
			return nil, err
		}
		muted = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			muted[id] = struct{}{}
		}
	}
	now := time.Now()
	var buf bytes.Buffer
	offset := 0
	for {
		rows, err := e.deps.FollowingRepo.ListFollowing(userID, csvBatchSize, offset)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, f := range rows {
			if muted != nil {
				if _, ok := muted[f.FolloweeID]; ok {
					continue
				}
			}
			u, err := e.deps.UserRepo.FindByID(f.FolloweeID)
			if err != nil || u == nil {
				continue
			}
			// upstream: u.updatedAt が存在し、かつ 90日より古ければ inactive。
			// updatedAt が nil (未更新) の user は除外しない。
			if excludeInactive && u.UpdatedAt != nil && now.Sub(*u.UpdatedAt) > inactiveThreshold {
				continue
			}
			fmt.Fprintf(&buf, "%s,withReplies=%s\n", acct(u), strconv.FormatBool(f.WithReplies))
		}
		if len(rows) < csvBatchSize {
			break
		}
		offset += csvBatchSize
	}
	return buf.Bytes(), nil
}

// exportBlocking emits one `acct` line per blocked user.
func (e *Exporter) exportBlocking(userID string) ([]byte, error) {
	var buf bytes.Buffer
	offset := 0
	for {
		rows, err := e.deps.BlockingRepo.ListByBlocker(userID, "", "", csvBatchSize, offset)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, b := range rows {
			u, err := e.deps.UserRepo.FindByID(b.BlockeeID)
			if err != nil || u == nil {
				continue
			}
			fmt.Fprintf(&buf, "%s\n", acct(u))
		}
		if len(rows) < csvBatchSize {
			break
		}
		offset += csvBatchSize
	}
	return buf.Bytes(), nil
}

// exportMuting emits one `acct` line per muted user, skipping entries with an
// ExpiresAt set (本家と同じく永続ミュートのみをエクスポート)。
func (e *Exporter) exportMuting(userID string) ([]byte, error) {
	var buf bytes.Buffer
	offset := 0
	for {
		rows, err := e.deps.MutingRepo.ListByMuter(userID, "", "", csvBatchSize, offset)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, m := range rows {
			if m.ExpiresAt != nil {
				continue
			}
			u, err := e.deps.UserRepo.FindByID(m.MuteeID)
			if err != nil || u == nil {
				continue
			}
			fmt.Fprintf(&buf, "%s\n", acct(u))
		}
		if len(rows) < csvBatchSize {
			break
		}
		offset += csvBatchSize
	}
	return buf.Bytes(), nil
}

// exportUserLists emits `listName,acct,withReplies=(true|false)` per (list, member).
// withReplies は現状 false 固定 (UserListMembership に withReplies 列がない)。
// 将来的にカラム追加された場合はここを更新する。
func (e *Exporter) exportUserLists(userID string) ([]byte, error) {
	var buf bytes.Buffer
	lists, err := e.deps.UserListRepo.ListByUser(userID)
	if err != nil {
		return nil, err
	}
	for _, list := range lists {
		members, err := e.deps.UserListRepo.ListMembers(list.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			u := m.User
			if u == nil {
				u2, err := e.deps.UserRepo.FindByID(m.UserID)
				if err != nil || u2 == nil {
					continue
				}
				u = u2
			}
			fmt.Fprintf(&buf, "%s,%s,withReplies=false\n", list.Name, acct(u))
		}
	}
	return buf.Bytes(), nil
}
