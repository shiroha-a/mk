// Package instance provides services for managing remote ActivityPub
// instances and their lifecycle metadata.
package instance

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

	// mu は suspendCache / requestReceivedCache / lastMetaWarn を保護する。
	// ShouldSkipDelivery / MarkRequestReceived は deliver / inbox hot path から
	// 並行に呼ばれるため軽量な Mutex で囲う (#1407 / #1410 / #1429)。
	mu sync.Mutex
	// suspendCache は host -> suspend 判定 (bool) の短期キャッシュ。deliver hot
	// path の FindByHost DB 往復を cacheTTL の間だけ省く (#1407)。
	suspendCache map[string]suspendEntry
	cacheTTL     time.Duration
	// requestReceivedCache は host -> 窓失効時刻 (= 最終書込 + requestReceivedWindow)。
	// MarkRequestReceived の latestRequestReceivedAt 書込を host あたり窓内 1 回に
	// 間引く。窓内は SELECT も UPDATE も省く。Misskey TS InboxProcessorService の
	// CollapsedQueue (本番 5 分窓) 相当 (#1429)。
	requestReceivedCache  map[string]time.Time
	requestReceivedWindow time.Duration
	// responseHealthyCache は host -> "isNotResponding==false と判定した窓" の失効
	// 時刻。RecordResponseSuccess の per-deliver FindByHost SELECT を窓内省く
	// (#1429 review)。TS DeliverProcessorService は instance を RedisKVCache から
	// 取り healthy deliver で DB を引かないのに合わせ、健全 host への連続配送で
	// SELECT/UPDATE とも 0 にする。isNotResponding==true への遷移は
	// RecordResponseError だけ (確認済み) なので、そこで invalidate すれば整合する。
	responseHealthyCache map[string]time.Time
	responseHealthyTTL   time.Duration
	// lastMetaWarn / metaWarnEvery は meta.Fetch 失敗 warn のレート制限用。
	// 継続障害時に配送ごとに warn を吐かないよう間引く (#1410)。
	lastMetaWarn  time.Time
	metaWarnEvery time.Duration
}

// suspendEntry caches a host's delivery-suspend decision until expiresAt.
type suspendEntry struct {
	skip      bool
	expiresAt time.Time
}

// NewService constructs an instance Service.
func NewService(repo repository.InstanceRepository, metaRepo repository.MetaRepository, idGen id.Generator) *Service {
	return &Service{
		repo:                  repo,
		metaRepo:              metaRepo,
		idGen:                 idGen,
		clock:                 time.Now,
		suspendCache:          make(map[string]suspendEntry),
		cacheTTL:              5 * time.Minute,
		requestReceivedCache:  make(map[string]time.Time),
		requestReceivedWindow: 5 * time.Minute,
		responseHealthyCache:  make(map[string]time.Time),
		responseHealthyTTL:    5 * time.Minute,
		metaWarnEvery:         time.Minute,
	}
}

// SetClock overrides the time source. Intended for tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

// SetSuspendCacheTTL overrides the suspend-decision cache TTL. Intended for
// tests that need to exercise cache expiry deterministically (#1407).
func (s *Service) SetSuspendCacheTTL(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheTTL = d
}

// SetMetaWarnEvery overrides the meta-fetch warning rate-limit window.
// Intended for tests (#1410).
func (s *Service) SetMetaWarnEvery(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metaWarnEvery = d
}

// SetRequestReceivedWindow overrides the MarkRequestReceived collapse window.
// Intended for tests that exercise the throttle boundary deterministically
// (#1429).
func (s *Service) SetRequestReceivedWindow(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestReceivedWindow = d
}

// SetResponseHealthyTTL overrides the RecordResponseSuccess healthy-state cache
// TTL. Intended for tests that exercise the SELECT-skip boundary
// deterministically (#1429 review).
func (s *Service) SetResponseHealthyTTL(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responseHealthyTTL = d
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
//
// Misskey TS の InboxProcessorService は受信を CollapsedQueue (本番 5 分窓) で
// 集約し、host あたり最大 5 分に 1 回だけ UPDATE する (#1429)。mk-go も同様に
// requestReceivedWindow 窓で間引く: 窓内の再呼び出しは FindByHost (SELECT) と
// UpdateFields (UPDATE) を両方スキップして、inbox 最頻ジョブの per-job 2 往復を
// 削る。窓越え (または初回) のみ書き込み、その時刻 + window を窓失効時刻として
// 記録する。
func (s *Service) MarkRequestReceived(host string) error {
	if host == "" {
		return nil
	}
	now := s.clock()
	// 窓内に既に書込済みなら SELECT/UPDATE とも省く (TS CollapsedQueue 相当)。
	s.mu.Lock()
	if exp, ok := s.requestReceivedCache[host]; ok && now.Before(exp) {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if _, err := s.repo.FindByHost(host); err != nil {
		// 未登録 host はキャッシュせず次回も SELECT で確認する (登録後に窓集約が
		// 効き始める)。
		return nil
	}
	if err := s.repo.UpdateFields(host, map[string]any{
		"latestRequestReceivedAt": &now,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	// 書き込みついでに期限切れ entry を amortized に掃除する (suspendCache と同方針)。
	s.evictExpiredRequestReceivedLocked(now)
	s.requestReceivedCache[host] = now.Add(s.requestReceivedWindow)
	s.mu.Unlock()
	return nil
}

// evictExpiredRequestReceivedLocked drops collapse-window entries whose window
// has elapsed. Must be called with s.mu held. host cardinality は連合先数で有界
// なので全走査で十分 (suspendCache の evictExpiredLocked と同方針, #1429)。
func (s *Service) evictExpiredRequestReceivedLocked(now time.Time) {
	for h, exp := range s.requestReceivedCache {
		if !now.Before(exp) {
			delete(s.requestReceivedCache, h)
		}
	}
}

// RecordResponseSuccess marks the instance as responsive again, clearing
// notRespondingSince. deliver の配信成功時に呼ばれる。
//
// Misskey TS DeliverProcessorService は isNotResponding === true の状態遷移時
// のみ update し、正常応答が続く間は DB 書込をしない (#1429)。mk-go も
// FindByHost 結果が既に isNotResponding == false なら UPDATE をスキップする。
//
// さらに deliver 成功 hot path の per-job FindByHost SELECT も responseHealthyCache
// で responseHealthyTTL 窓内省く (#1429 review)。健全 host への連続配送では窓内
// 2 回目以降は SELECT も UPDATE も走らない (TS が RedisKVCache 経由で DB を引かない
// のと同等)。not-responding への遷移は RecordResponseError が cache を invalidate
// するため、回復 UPDATE を stale healthy で取りこぼさない。
func (s *Service) RecordResponseSuccess(host string) error {
	if host == "" {
		return nil
	}
	now := s.clock()
	// 窓内に healthy と判定済みなら SELECT/UPDATE とも省く。
	s.mu.Lock()
	if exp, ok := s.responseHealthyCache[host]; ok && now.Before(exp) {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	inst, err := s.repo.FindByHost(host)
	if err != nil {
		// 未登録 host はキャッシュしない (次回も確認)。
		return nil
	}
	// not-responding からの回復遷移時のみ UPDATE する (TS の状態遷移ガード)。
	if inst.IsNotResponding {
		if err := s.repo.UpdateFields(host, map[string]any{
			"isNotResponding":    false,
			"notRespondingSince": (*time.Time)(nil),
		}); err != nil {
			return err
		}
	}
	// healthy と確定したので窓キャッシュに載せ、以降の SELECT を省く。
	s.mu.Lock()
	s.evictExpiredResponseHealthyLocked(now)
	s.responseHealthyCache[host] = now.Add(s.responseHealthyTTL)
	s.mu.Unlock()
	return nil
}

// evictExpiredResponseHealthyLocked drops healthy-state cache entries whose
// window has elapsed. Must be called with s.mu held (#1429 review)。
func (s *Service) evictExpiredResponseHealthyLocked(now time.Time) {
	for h, exp := range s.responseHealthyCache {
		if !now.Before(exp) {
			delete(s.responseHealthyCache, h)
		}
	}
}

// RecordResponseError marks the instance as not responding. 既に not responding
// 状態であれば notRespondingSince を更新せず維持する (TS 準拠、状態遷移時のみ
// 書込)。状態遷移時は RecordResponseSuccess の responseHealthyCache を invalidate
// して、回復記録が stale healthy で取りこぼされないようにする (#1429 review)。
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
	if err := s.repo.UpdateFields(host, map[string]any{
		"isNotResponding":    true,
		"notRespondingSince": &now,
	}); err != nil {
		return err
	}
	// healthy-cache を無効化する。これが無いと、直後の RecordResponseSuccess が
	// stale な healthy 判定で回復 UPDATE を取りこぼし、TTL が切れるまで
	// isNotResponding が解除されない。isNotResponding==true への遷移は本メソッド
	// だけなので、ここで消せば healthy-cache の整合が保てる (#1429 review)。
	s.mu.Lock()
	delete(s.responseHealthyCache, host)
	s.mu.Unlock()
	return nil
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

// IsMediaSilenced reports whether the host matches an entry in
// meta.mediaSilencedHosts. Used by reaction gating to reject custom emoji
// reactions from media-silenced remote hosts (#1538).
func (s *Service) IsMediaSilenced(host string) bool {
	if host == "" {
		return false
	}
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return false
	}
	return HostMatchesAny(meta.MediaSilencedHosts, host)
}

// FederationHostSets bundles the host lists that drive the federation panel's
// blocked / silenced / media-silenced state. 3 つとも同じ []string なので、
// 位置引数で取り違えないよう named field の struct にまとめる。
type FederationHostSets struct {
	Blocked       []string
	Silenced      []string
	MediaSilenced []string
	// SuspendedSoftware は meta.deliverSuspendedSoftware をパースしたもの。
	// federation/instances 等の softwareSuspended 表示判定に使う (#1732)。
	SuspendedSoftware []model.SuspendedSoftwareEntry
}

// FederationHostLists returns the blocked / silenced / media-silenced host
// lists from meta. federation/instances ハンドラがフィルタ突合と、レスポンスの
// isBlocked / isSilenced / isMediaSilenced 算出の双方で使うため、meta を 1 回
// だけ読んで使い回す入口。
//
// IsBlocked / IsSilenced (inbox hot path) はベストエフォートで meta error を
// 握り潰すが、こちらは admin endpoint 専用なので error をそのまま返す: meta が
// 読めない状況は本家 TS でも 500 になり (metaService.fetch が throw)、空一覧を
// 200 で返して「ブロックなし」と誤表示するより安全。
func (s *Service) FederationHostLists() (FederationHostSets, error) {
	meta, err := s.metaRepo.Fetch()
	if err != nil {
		return FederationHostSets{}, err
	}
	// deliverSuspendedSoftware (jsonb) を softwareSuspended 判定用に parse する
	// (#1732)。不正な JSON は空扱い (best-effort)。
	var suspended []model.SuspendedSoftwareEntry
	if len(meta.DeliverSuspendedSoftware) > 0 {
		_ = json.Unmarshal(meta.DeliverSuspendedSoftware, &suspended)
	}
	return FederationHostSets{
		Blocked:           meta.BlockedHosts,
		Silenced:          meta.SilencedHosts,
		MediaSilenced:     meta.MediaSilencedHosts,
		SuspendedSoftware: suspended,
	}, nil
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

// ShouldSkipDelivery reports whether ActivityPub delivery to host must be
// skipped because the remote instance is blocked, excluded by the federation
// mode, or administratively suspended (suspensionState != none). 配送の
// dispatch hot path から呼ばれる (#1404)。
//
// meta は 1 回だけ Fetch する。IsBlocked + IsAllowed を別々に呼ぶと meta.Fetch
// が 2 回走り blockedHosts も二重評価されるため、hot path 向けにここで判定を
// インライン化した (#1406 review)。meta.Fetch / instance lookup の error は
// IsBlocked / IsAllowed と同じく fail-open (= skip しない) に倒す (一時的な
// DB error で連合が止まる drop-in 劣化を避ける)。
func (s *Service) ShouldSkipDelivery(host string) bool {
	if host == "" {
		return false
	}
	if meta, err := s.metaRepo.Fetch(); err == nil {
		switch meta.Federation {
		case "none":
			return true
		case "specified":
			if !HostMatchesAny(meta.FederationHosts, host) {
				return true
			}
		}
		if HostMatchesAny(meta.BlockedHosts, host) {
			return true
		}
	} else {
		// fail-open (IsBlocked / IsAllowed と同方針) だが、meta が読めない間は
		// blockedHosts / federation mode の判定が丸ごと抜けて block 先へ配送し
		// 得る。モデレーション制御の漏れなので運用で気付けるよう warn に残す。
		// suspensionState (DB 列) の判定は後段で引き続き効く (#1406 review)。
		// 継続障害時に配送ごとに warn を吐かないようレート制限する (#1410)。
		s.warnMetaFetchFailed(host, err)
	}
	return s.shouldSkipBySuspend(host)
}

// shouldSkipBySuspend returns the cached suspend decision for host, looking it
// up from the repository only on a cache miss or after cacheTTL has elapsed.
// deliver hot path の FindByHost DB 往復を cacheTTL の間だけ省く (#1407)。
func (s *Service) shouldSkipBySuspend(host string) bool {
	now := s.clock()

	s.mu.Lock()
	if e, ok := s.suspendCache[host]; ok && now.Before(e.expiresAt) {
		s.mu.Unlock()
		return e.skip
	}
	s.mu.Unlock()

	skip := s.lookupSuspended(host)

	s.mu.Lock()
	// cache miss / 期限切れ時に書き込むついでに、既に期限切れの他 entry も
	// 掃除する。配送先 host は有界 (連合先数) だが、過去に配送した host が
	// 二度と来ない場合でも map に滞留し続けないよう、書き込みコストに相乗り
	// させた amortized なエビクションで上限を抑える (#1407 review)。
	s.evictExpiredLocked(now)
	s.suspendCache[host] = suspendEntry{skip: skip, expiresAt: now.Add(s.cacheTTL)}
	s.mu.Unlock()

	return skip
}

// evictExpiredLocked drops cache entries whose TTL has elapsed. Must be called
// with s.mu held. host cardinality は有界なので全走査で十分 (#1407 review)。
func (s *Service) evictExpiredLocked(now time.Time) {
	for h, e := range s.suspendCache {
		if !now.Before(e.expiresAt) {
			delete(s.suspendCache, h)
		}
	}
}

// InvalidateSuspendCache drops the cached suspend decision for host so the next
// ShouldSkipDelivery re-reads it from the repository. suspend / unsuspend 直後に
// 呼ぶことで、TTL を待たずに配送可否へ即時反映する (#1407 review)。Service.Suspend
// 経由だけでなく、admin/federation/update-instance のように instanceRepo を直接
// 更新する handler からも呼べるよう公開している。
func (s *Service) InvalidateSuspendCache(host string) {
	if host == "" {
		return
	}
	s.mu.Lock()
	delete(s.suspendCache, host)
	s.mu.Unlock()
}

// SuspendCacheLen returns the number of entries currently held in the suspend
// decision cache. Intended for tests asserting eviction behaviour (#1407 review).
func (s *Service) SuspendCacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.suspendCache)
}

// lookupSuspended resolves the suspend decision for host directly from the
// repository. A missing row or lookup error is fail-open (not skipped), matching
// the previous inline behaviour.
func (s *Service) lookupSuspended(host string) bool {
	inst, err := s.repo.FindByHost(host)
	if err != nil {
		return false
	}
	return inst.SuspensionState != "" && inst.SuspensionState != model.SuspensionStateNone
}

// warnMetaFetchFailed logs a meta-fetch failure at most once per metaWarnEvery
// window so a persistent meta outage does not emit a warning per delivery job.
// The first failure always logs to preserve observability (#1410).
func (s *Service) warnMetaFetchFailed(host string, err error) {
	now := s.clock()

	s.mu.Lock()
	if !s.lastMetaWarn.IsZero() && now.Sub(s.lastMetaWarn) < s.metaWarnEvery {
		s.mu.Unlock()
		return
	}
	s.lastMetaWarn = now
	s.mu.Unlock()

	slog.Warn("instance: ShouldSkipDelivery could not fetch meta; block/federation gate skipped this call",
		"host", host, "error", err)
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
	if err := s.repo.UpdateFields(host, map[string]any{
		"suspensionState": state,
	}); err != nil {
		return err
	}
	// suspend / unsuspend の結果を配送可否へ TTL を待たず即時反映する (#1407 review)。
	s.InvalidateSuspendCache(host)
	return nil
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
