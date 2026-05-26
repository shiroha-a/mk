// Package instance provides services for managing remote ActivityPub
// instances and their lifecycle metadata.
package instance

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// hostMatchCaser folds host names case-insensitively in a Unicode-aware
// manner. Misskey TS uses String.prototype.toLowerCase() which works on
// arbitrary Unicode (e.g. \`Ä\` → \`ä\`); strings.ToLower in Go only handles
// ASCII, so raw IDN representations would diverge between TS and mk-go
// without this caser. cases.Caser instances are safe for concurrent use.
var hostMatchCaser = cases.Lower(language.Und)

// Errors returned by Service.
var (
	// ErrInstanceNotFound is returned when no instance row matches the host.
	ErrInstanceNotFound = errors.New("instance not found")
)

// MetadataFetcher fetches and persists nodeinfo for a newly discovered host.
// 循環依存を避けるため interface で受け取る (実装は同じ package の
// FetchMetadataService または stub)。
type MetadataFetcher interface {
	Fetch(host string) error
}

// Service manages the local view of remote instances. It is the entry point
// for any code that needs to know whether a host is blocked / silenced /
// suspended, and for refreshing the cached metadata after a fetch.
type Service struct {
	repo            repository.InstanceRepository
	metaRepo        repository.MetaRepository
	idGen           id.Generator
	clock           func() time.Time
	metadataFetcher MetadataFetcher
}

// NewService constructs an instance Service.
func NewService(repo repository.InstanceRepository, metaRepo repository.MetaRepository, idGen id.Generator) *Service {
	return &Service{
		repo:     repo,
		metaRepo: metaRepo,
		idGen:    idGen,
		clock:    time.Now,
	}
}

// SetClock overrides the time source. Intended for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// SetMetadataFetcher attaches a MetadataFetcher invoked best-effort after a
// new instance row is created via RegisterFromHost.
func (s *Service) SetMetadataFetcher(f MetadataFetcher) {
	s.metadataFetcher = f
}

// RegisterFromHost ensures an instance row exists for the given host. If a row
// already exists it is returned as-is; otherwise a fresh row is created with
// firstRetrievedAt set to now and usersCount = 1. 新規作成成功時には
// metadataFetcher (設定されていれば) を best-effort で呼び nodeinfo を取り込む。
//
// 呼び出し元: Resolver でリモートユーザーを新規取り込みした直後。
func (s *Service) RegisterFromHost(host string) (*model.Instance, error) {
	if host == "" {
		return nil, errors.New("host is required")
	}
	if existing, err := s.repo.FindByHost(host); err == nil {
		return existing, nil
	}
	now := s.clock()
	inst := &model.Instance{
		ID:               s.idGen.Generate(now),
		Host:             host,
		FirstRetrievedAt: now,
		UsersCount:       1,
		SuspensionState:  model.SuspensionStateNone,
	}
	if err := s.repo.Create(inst); err != nil {
		return nil, err
	}
	if s.metadataFetcher != nil {
		// nodeinfo 取得の失敗は致命的ではない (次回再試行可能なので無視)。
		_ = s.metadataFetcher.Fetch(host)
	}
	return inst, nil
}

// MarkRequestReceived bumps the latestRequestReceivedAt timestamp on the
// instance row. ホストが未登録の場合は黙って no-op。Inbox handler から呼ぶ。
func (s *Service) MarkRequestReceived(host string) error {
	if host == "" {
		return nil
	}
	if _, err := s.repo.FindByHost(host); err != nil {
		return nil
	}
	now := s.clock()
	return s.repo.UpdateFields(host, map[string]any{
		"latestRequestReceivedAt": &now,
	})
}

// RecordResponseSuccess marks the instance as responsive again, clearing
// notRespondingSince. Outbox の配信成功時 (Phase 4 で wire 予定) に呼ばれる。
func (s *Service) RecordResponseSuccess(host string) error {
	if host == "" {
		return nil
	}
	if _, err := s.repo.FindByHost(host); err != nil {
		return nil
	}
	return s.repo.UpdateFields(host, map[string]any{
		"isNotResponding":    false,
		"notRespondingSince": (*time.Time)(nil),
	})
}

// RecordResponseError marks the instance as not responding. 既に not responding
// 状態であれば notRespondingSince を更新せず維持する。
func (s *Service) RecordResponseError(host string) error {
	if host == "" {
		return nil
	}
	inst, err := s.repo.FindByHost(host)
	if err != nil {
		return nil
	}
	if inst.IsNotResponding {
		return nil
	}
	now := s.clock()
	return s.repo.UpdateFields(host, map[string]any{
		"isNotResponding":    true,
		"notRespondingSince": &now,
	})
}

// IsBlocked reports whether the host matches an entry in meta.blockedHosts.
// meta が読めない場合は false を返す (ベストエフォート)。
func (s *Service) IsBlocked(host string) bool {
	if host == "" {
		return false
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return false
	}
	return HostMatchesAny(meta.BlockedHosts, host)
}

// IsSilenced reports whether the host matches an entry in meta.silencedHosts.
func (s *Service) IsSilenced(host string) bool {
	if host == "" {
		return false
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return false
	}
	return HostMatchesAny(meta.SilencedHosts, host)
}

// FederationHostLists returns the blocked / silenced / media-silenced host
// lists from meta. federation/instances ハンドラがフィルタ突合と、レスポンスの
// isBlocked / isSilenced / isMediaSilenced 算出の双方で使うため、meta を 1 回
// だけ読んで使い回す入口。meta が読めない場合は nil を返す (ベストエフォート;
// admin federation 一覧が transient error で完全に落ちるのを避ける)。
func (s *Service) FederationHostLists() (blocked, silenced, mediaSilenced []string) {
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return nil, nil, nil
	}
	return meta.BlockedHosts, meta.SilencedHosts, meta.MediaSilencedHosts
}

// IsAllowed reports whether the local instance is willing to federate with
// the given host. Misskey TS の admin/federation 設定と同じく、3 段階の
// federation mode + blockedHosts の組み合わせで判定する (#536)。
//
//   - federation == "none": 連合無効、全 host を deny
//   - federation == "specified": federationHosts に列挙した host (または
//     その配下サブドメイン) だけ allow
//   - その他 ("all" / 既定): blockedHosts に含まれない host を allow
//
// 引数の host が空文字 (= local) は常に true を返す。meta が読めない場合は
// 安全側に倒さず true を返す (起動時の transient error で連合が落ちると
// drop-in 互換が大幅に崩れるため、ベストエフォートを優先)。
//
// host とリスト要素の比較は Misskey TS UtilityService と同じ
// **case-insensitive な suffix match** で行う (#590)。例えば
// federationHosts に \`example.com\` を入れると \`example.com\` 自身に加え
// \`sub.example.com\` も透過する。`x` 単独で `.foo.x` も `bar.x` も拾えるよう、
// 比較は \`.\` を前置した文字列同士の endsWith。これにより whitelist 設定が
// 直感どおりに効く。
func (s *Service) IsAllowed(host string) bool {
	if host == "" {
		return true
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return true
	}
	switch meta.Federation {
	case "none":
		return false
	case "specified":
		if !HostMatchesAny(meta.FederationHosts, host) {
			return false
		}
	}
	return !HostMatchesAny(meta.BlockedHosts, host)
}

// HostMatchesAny reports whether host (case-insensitive, Unicode-aware)
// matches any of the given patterns under Misskey TS's suffix-match rule.
// A pattern matches if host equals it, or host ends with `.<pattern>`
// (i.e. host is a subdomain).
//
// 比較ロジックは TS の \`UtilityService.isBlockedHost\` (および
// \`isFederationAllowedHost\`) と等価:
//
//	patterns.some(x => `.${host.toLowerCase()}`.endsWith(`.${x.toLowerCase()}`))
//
// host が空文字なら常に false。空 pattern は意図的に skip する (\`host == ""\`
// ガードが既に効くので "." 単独が任意 host に誤マッチすることは無いが、
// admin が誤って空エントリを混入させた場合に "全 host を allow / block" に
// 化けない defensive guard を残す)。
func HostMatchesAny(patterns []string, host string) bool {
	if host == "" {
		return false
	}
	needle := "." + hostMatchCaser.String(host)
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.HasSuffix(needle, "."+hostMatchCaser.String(p)) {
			return true
		}
	}
	return false
}

// Suspend updates the suspensionState column for the host. 引数の state には
// model.SuspensionStateManuallySuspended などを渡す。
func (s *Service) Suspend(host string, state model.SuspensionState) error {
	if host == "" {
		return errors.New("host is required")
	}
	if _, err := s.repo.FindByHost(host); err != nil {
		return ErrInstanceNotFound
	}
	return s.repo.UpdateFields(host, map[string]any{
		"suspensionState": state,
	})
}

// UpdateModerationNote sets the moderationNote field on the instance row.
func (s *Service) UpdateModerationNote(host, note string) error {
	if host == "" {
		return errors.New("host is required")
	}
	if _, err := s.repo.FindByHost(host); err != nil {
		return ErrInstanceNotFound
	}
	return s.repo.UpdateFields(host, map[string]any{
		"moderationNote": note,
	})
}

// FindByHost returns the instance row for the given host or ErrInstanceNotFound.
func (s *Service) FindByHost(host string) (*model.Instance, error) {
	inst, err := s.repo.FindByHost(host)
	if err != nil {
		// gorm.ErrRecordNotFound のみ ErrInstanceNotFound に正規化、それ以外
		// (DB connection error 等) は呼び出し側で 500 / alert に流せるよう
		// raw err を保つ。旧実装は全 err を ErrInstanceNotFound に潰していて
		// 観測性が落ちていた (#915 review)。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	return inst, nil
}

// List returns instances matching the filter.
func (s *Service) List(filter model.InstanceListFilter) ([]*model.Instance, error) {
	return s.repo.List(filter)
}
