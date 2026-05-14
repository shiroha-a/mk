package transfer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// FollowingService is the subset of core/following.Service used by imports.
//
// FollowOptions 引数 (#1058) で CSV row 由来の withReplies 等の preference を
// threading する。adapter layer で corefollowing.FollowOptions に bridge する
// ため transfer package 単位で interface を切る (= core/following 直接依存を
// 避ける)。
type FollowingService interface {
	Follow(followerID, followeeID string, opts FollowOptions) (any, error)
}

// FollowOptions mirrors the relevant subset of corefollowing.FollowOptions
// that CSV imports need to threading. Currently only WithReplies is parsed
// from the CSV row.
type FollowOptions struct {
	WithReplies bool
}

// BlockingService is the subset of core/blocking.Service used by imports.
type BlockingService interface {
	Block(blockerID, blockeeID string) (*model.Blocking, error)
}

// MutingService is the subset of core/muting.Service used by imports.
type MutingService interface {
	Mute(muterID, muteeID string, expiresAt *time.Time) (*model.Muting, error)
}

// ImporterDeps bundles the dependencies needed by Importer.
type ImporterDeps struct {
	UserRepo     repository.UserRepository
	UserListRepo repository.UserListRepository
	AntennaRepo  repository.AntennaRepository
	Drive        DriveReader
	Following    FollowingService
	Blocking     BlockingService
	Muting       MutingService
	Notifier     NotificationDispatcher
	IDGen        id.Generator
	SelfHost     string // instance host for detecting local acct entries
}

// Importer reads a previously-uploaded DriveFile and applies the contained
// operations to the requesting user's account.
type Importer struct {
	deps ImporterDeps
}

// NewImporter constructs an Importer from the given dependencies.
func NewImporter(deps ImporterDeps) *Importer {
	return &Importer{deps: deps}
}

// ImportResult captures per-item counters returned to the caller and included
// in the completion notification.
type ImportResult struct {
	Total   int
	Applied int
	Skipped int
}

// Import reads the DriveFile referenced by fileID and dispatches the content
// to the handler matching importType. Errors per-item are logged and counted
// as Skipped so that a single malformed entry does not abort the batch.
func (i *Importer) Import(ctx context.Context, userID, importType, fileID string) (*ImportResult, error) {
	user, err := i.deps.UserRepo.FindByID(userID)
	if err != nil {
		return nil, fmt.Errorf("find user: %w", err)
	}
	if i.deps.Drive == nil {
		return nil, errors.New("drive reader not configured")
	}
	_, body, err := i.deps.Drive.Fetch(fileID)
	if err != nil {
		return nil, fmt.Errorf("fetch drive file: %w", err)
	}

	var result *ImportResult
	switch importType {
	case ImportFollowing:
		result, err = i.importFollowing(user, body)
	case ImportBlocking:
		result, err = i.importBlocking(user, body)
	case ImportMuting:
		result, err = i.importMuting(user, body)
	case ImportUserLists:
		result, err = i.importUserLists(user, body)
	case ImportAntennas:
		result, err = i.importAntennas(user, body)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, importType)
	}
	if err != nil {
		return nil, err
	}

	if i.deps.Notifier != nil {
		_, _ = i.deps.Notifier.Create(ctx, notification.CreateInput{
			NotifieeID: user.ID,
			Type:       notification.TypeImportCompleted,
			Extra: map[string]any{
				"importedEntity": importType,
				"total":          result.Total,
				"applied":        result.Applied,
				"skipped":        result.Skipped,
			},
		})
	}
	return result, nil
}

// parseAcct splits a "username" or "username@host" string into lowercase
// username and (optional) host. Returns an error if the string is empty or
// malformed.
func parseAcct(s string) (username string, host *string, err error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "@"))
	if s == "" {
		return "", nil, errors.New("empty acct")
	}
	at := strings.IndexByte(s, '@')
	if at < 0 {
		return strings.ToLower(s), nil, nil
	}
	username = strings.ToLower(s[:at])
	h := strings.ToLower(s[at+1:])
	if username == "" || h == "" {
		return "", nil, errors.New("malformed acct")
	}
	return username, &h, nil
}

// resolveTargetUser looks up a user by acct. Remote users must already exist
// locally; webfinger-based resolution is intentionally out of scope for this
// package (documented limitation).
func (i *Importer) resolveTargetUser(acct string) (*model.User, error) {
	username, host, err := parseAcct(acct)
	if err != nil {
		return nil, err
	}
	// selfHost と一致するホストは local 扱いにする
	if host != nil && i.deps.SelfHost != "" && *host == strings.ToLower(i.deps.SelfHost) {
		host = nil
	}
	return i.deps.UserRepo.FindByUsernameLower(username, host)
}

// scanCSV yields non-empty trimmed lines from a CSV body.
func scanCSV(body []byte) []string {
	s := bufio.NewScanner(bytes.NewReader(body))
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	out := make([]string, 0, 32)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// logSkip emits a warning log for an import entry that was skipped due to an
// error. 引数は呼び出し元が各行の位置を埋める想定。
func logSkip(importType, acct string, err error) {
	slog.Warn("transfer import: skipped entry",
		"type", importType, "target", acct, "err", err)
}
