package timeline

import (
	"github.com/shiroha-a/mk/internal/repository"
)

// metaRepoCacheLimits is the production MetaCacheLimitsProvider that reads
// the four cache-cap columns from the meta singleton on every call. The 4
// integer columns are cheap to fetch and the call site (FanoutHook.OnNoteCreated)
// is invoked once per note creation, so we accept the per-note round-trip
// rather than maintain an explicit cache that would need invalidation when
// the meta row is updated.
type metaRepoCacheLimits struct {
	repo repository.MetaRepository
}

// NewMetaRepoCacheLimits constructs a MetaCacheLimitsProvider backed by the
// given MetaRepository.
func NewMetaRepoCacheLimits(repo repository.MetaRepository) MetaCacheLimitsProvider {
	return &metaRepoCacheLimits{repo: repo}
}

// NewMetaFanoutToggle constructs a FanoutToggleProvider backed by the given
// MetaRepository. 通常は router から CachedMetaRepository が渡るので、
// note 作成ごと / timeline 取得ごとの参照でも in-memory 参照で済む
// (admin/update-meta の Update で即 invalidate される)。
func NewMetaFanoutToggle(repo repository.MetaRepository) FanoutToggleProvider {
	return &metaRepoCacheLimits{repo: repo}
}

// FanoutTimelineEnabled implements FanoutToggleProvider. meta を読めない場合は
// 有効側に倒す (既定値が true なので、一時的な DB エラーで fan-out が止まるより
// 従来どおり動くほうが害が小さい)。
func (m *metaRepoCacheLimits) FanoutTimelineEnabled() bool {
	if m == nil || m.repo == nil {
		return true
	}
	meta, err := m.repo.Fetch()
	if err != nil || meta == nil {
		return true
	}
	return meta.EnableFanoutTimeline
}

// CacheLimits implements MetaCacheLimitsProvider. Errors are swallowed and
// returned as zero values; resolveCap then falls back to the documented
// Misskey defaults.
func (m *metaRepoCacheLimits) CacheLimits() CacheLimits {
	if m == nil || m.repo == nil {
		return CacheLimits{}
	}
	meta, err := m.repo.Fetch()
	if err != nil || meta == nil {
		return CacheLimits{}
	}
	return CacheLimits{
		LocalUserUserTimeline:  meta.PerLocalUserUserTimelineCacheMax,
		RemoteUserUserTimeline: meta.PerRemoteUserUserTimelineCacheMax,
		UserHomeTimeline:       meta.PerUserHomeTimelineCacheMax,
		UserListTimeline:       meta.PerUserListTimelineCacheMax,
	}
}
