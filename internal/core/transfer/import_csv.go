package transfer

import (
	"fmt"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

// importFollowing parses a CSV body of `acct[,key=value...]` lines and applies
// a follow for each. Remote users must already exist locally.
//
// Supported optional fields (key=value form, comma-separated after the acct):
//   - `withReplies=true|false`: initial value for `following.withReplies`. Any
//     other value (or absence) defaults to false (= upstream-equivalent loose
//     parsing, matching `value === 'true'` semantics).
//
// upstream Misskey TS の export-following CSV は `acct,withReplies=BOOL` 2 fields
// で row を出力する (ExportFollowingProcessorService.ts:98)。upstream の import
// 側 (ImportFollowingProcessorService.ts:75) は `parts.slice(2)` で parse する
// バグがあり 2-fields export と整合しないが、mk-go は `parts[1:]` から parse して
// 実 export format と一致させる。
func (i *Importer) importFollowing(user *model.User, body []byte) (*ImportResult, error) {
	if i.deps.Following == nil {
		return nil, fmt.Errorf("following service not configured")
	}
	lines := scanCSV(body)
	res := &ImportResult{Total: len(lines)}
	for _, line := range lines {
		acctStr, opts := parseFollowingCSVLine(line)
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
		if _, err := i.deps.Following.Follow(user.ID, target.ID, opts); err != nil {
			res.Skipped++
			logSkip(ImportFollowing, acctStr, err)
			continue
		}
		res.Applied++
	}
	return res, nil
}

// parseFollowingCSVLine splits a CSV row into its acct field and any optional
// `key=value` fields, returning the acct and a populated FollowOptions.
//
// 不正な field (= `=` を含まない trailing fragment や未知の key) は silently
// skip する (= upstream の `switch (key)` で default なしの挙動と一致)。
func parseFollowingCSVLine(line string) (string, FollowOptions) {
	parts := strings.Split(line, ",")
	acct := strings.TrimSpace(parts[0])
	var opts FollowOptions
	for _, kv := range parts[1:] {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			continue
		}
		switch k {
		case "withReplies":
			// upstream Misskey TS の `value === 'true'` と同 semantics: 文字列
			// "true" 以外は (空文字 / "false" / "invalid" 等) すべて false。
			opts.WithReplies = v == "true"
		}
	}
	return acct, opts
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
