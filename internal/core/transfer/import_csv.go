package transfer

import (
	"fmt"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// importFollowing parses a CSV body of `acct` lines and applies a follow for
// each. Remote users must already exist locally.
//
// upstream Misskey TS の CSV export 形式は `acct[,withReplies=bool]` を取るが、
// mk-go の current 実装は first field (acct) のみ parse し withReplies は
// default false で固定する。FollowOptions を threading する対応は #1056
// follow-up で実装予定 (adapters.go の TODO comment 参照)。
func (i *Importer) importFollowing(user *model.User, body []byte) (*ImportResult, error) {
	if i.deps.Following == nil {
		return nil, fmt.Errorf("following service not configured")
	}
	lines := scanCSV(body)
	res := &ImportResult{Total: len(lines)}
	for _, line := range lines {
		acctStr := strings.TrimSpace(strings.SplitN(line, ",", 2)[0])
		target, err := i.resolveTargetUser(acctStr)
		if err != nil || target == nil {
			res.Skipped++
			logSkip(ImportFollowing, acctStr, err)
			continue
		}
		if target.ID == user.ID {
			res.Skipped++
			continue
		}
		if _, err := i.deps.Following.Follow(user.ID, target.ID); err != nil {
			res.Skipped++
			logSkip(ImportFollowing, acctStr, err)
			continue
		}
		res.Applied++
	}
	return res, nil
}

// importBlocking parses `acct` lines and applies a block per entry.
func (i *Importer) importBlocking(user *model.User, body []byte) (*ImportResult, error) {
	if i.deps.Blocking == nil {
		return nil, fmt.Errorf("blocking service not configured")
	}
	lines := scanCSV(body)
	res := &ImportResult{Total: len(lines)}
	for _, line := range lines {
		target, err := i.resolveTargetUser(line)
		if err != nil || target == nil {
			res.Skipped++
			logSkip(ImportBlocking, line, err)
			continue
		}
		if target.ID == user.ID {
			res.Skipped++
			continue
		}
		if _, err := i.deps.Blocking.Block(user.ID, target.ID); err != nil {
			res.Skipped++
			logSkip(ImportBlocking, line, err)
			continue
		}
		res.Applied++
	}
	return res, nil
}

// importMuting parses `acct` lines and applies a permanent mute per entry.
// 本家と同様に ExpiresAt は nil (無期限) で作成する。
func (i *Importer) importMuting(user *model.User, body []byte) (*ImportResult, error) {
	if i.deps.Muting == nil {
		return nil, fmt.Errorf("muting service not configured")
	}
	lines := scanCSV(body)
	res := &ImportResult{Total: len(lines)}
	var noExpire *time.Time
	for _, line := range lines {
		target, err := i.resolveTargetUser(line)
		if err != nil || target == nil {
			res.Skipped++
			logSkip(ImportMuting, line, err)
			continue
		}
		if target.ID == user.ID {
			res.Skipped++
			continue
		}
		if _, err := i.deps.Muting.Mute(user.ID, target.ID, noExpire); err != nil {
			res.Skipped++
			logSkip(ImportMuting, line, err)
			continue
		}
		res.Applied++
	}
	return res, nil
}

// importUserLists parses `listName,acct[,withReplies=bool]` lines and creates
// or reuses a UserList per listName, adding the referenced user to it.
func (i *Importer) importUserLists(user *model.User, body []byte) (*ImportResult, error) {
	lines := scanCSV(body)
	res := &ImportResult{Total: len(lines)}

	// 既存の (name -> *UserList) キャッシュ。同じ名前のリストは共有する。
	cache := make(map[string]*model.UserList)
	if existing, err := i.deps.UserListRepo.ListByUser(user.ID); err == nil {
		for _, l := range existing {
			cache[l.Name] = l
		}
	}

	for _, line := range lines {
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 {
			res.Skipped++
			continue
		}
		listName := strings.TrimSpace(parts[0])
		acctStr := strings.TrimSpace(parts[1])
		if listName == "" || acctStr == "" {
			res.Skipped++
			continue
		}

		list, ok := cache[listName]
		if !ok {
			list = &model.UserList{
				ID:     i.deps.IDGen.Generate(time.Now()),
				UserID: user.ID,
				Name:   listName,
			}
			if err := i.deps.UserListRepo.Create(list); err != nil {
				res.Skipped++
				logSkip(ImportUserLists, acctStr, err)
				continue
			}
			cache[listName] = list
		}

		target, err := i.resolveTargetUser(acctStr)
		if err != nil || target == nil {
			res.Skipped++
			logSkip(ImportUserLists, acctStr, err)
			continue
		}
		membership := &model.UserListMembership{
			ID:         i.deps.IDGen.Generate(time.Now()),
			UserListID: list.ID,
			UserID:     target.ID,
		}
		if err := i.deps.UserListRepo.AddMember(membership); err != nil {
			res.Skipped++
			logSkip(ImportUserLists, acctStr, err)
			continue
		}
		res.Applied++
	}
	return res, nil
}
