package instance_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newService(t *testing.T) (*instance.Service, *testutil.MockInstanceRepository, *testutil.MockMetaRepository) {
	t.Helper()
	repo := testutil.NewMockInstanceRepository()
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{}
	idGen, _ := id.NewGenerator("aidx")
	return instance.NewService(repo, metaRepo, idGen), repo, metaRepo
}

func TestService_RegisterFromHost_New(t *testing.T) {
	svc, repo, _ := newService(t)
	got, err := svc.RegisterFromHost("alpha.example")
	require.NoError(t, err)
	assert.Equal(t, "alpha.example", got.Host)
	assert.Equal(t, 1, got.UsersCount)
	assert.NotEmpty(t, repo.Instances["alpha.example"])
}

func TestService_RegisterFromHost_Existing(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{
		ID: "i1", Host: "alpha.example", UsersCount: 5,
	}
	got, err := svc.RegisterFromHost("alpha.example")
	require.NoError(t, err)
	assert.Equal(t, 5, got.UsersCount)
}

func TestService_RegisterFromHost_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.RegisterFromHost("")
	assert.Error(t, err)
}

func TestService_RegisterFromHost_CreateError(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.CreateErr = errors.New("create failed")
	_, err := svc.RegisterFromHost("beta.example")
	assert.Error(t, err)
}

func TestService_MarkRequestReceived(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	require.NoError(t, svc.MarkRequestReceived("alpha.example"))
	require.NotNil(t, repo.Instances["alpha.example"].LatestRequestReceivedAt)
}

func TestService_MarkRequestReceived_UnknownHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.MarkRequestReceived("missing.example"))
}

func TestService_MarkRequestReceived_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.MarkRequestReceived(""))
}

func TestService_RecordResponseError(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	require.NoError(t, svc.RecordResponseError("alpha.example"))
	assert.True(t, repo.Instances["alpha.example"].IsNotResponding)
	require.NotNil(t, repo.Instances["alpha.example"].NotRespondingSince)
}

func TestService_RecordResponseError_AlreadyNotResponding(t *testing.T) {
	svc, repo, _ := newService(t)
	since := time.Now().Add(-1 * time.Hour)
	repo.Instances["alpha.example"] = &model.Instance{
		ID: "i1", Host: "alpha.example",
		IsNotResponding: true, NotRespondingSince: &since,
	}
	require.NoError(t, svc.RecordResponseError("alpha.example"))
	assert.Equal(t, &since, repo.Instances["alpha.example"].NotRespondingSince)
}

func TestService_RecordResponseError_UnknownHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.RecordResponseError("missing.example"))
}

func TestService_RecordResponseError_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.RecordResponseError(""))
}

func TestService_RecordResponseSuccess(t *testing.T) {
	svc, repo, _ := newService(t)
	since := time.Now().Add(-1 * time.Hour)
	repo.Instances["alpha.example"] = &model.Instance{
		ID: "i1", Host: "alpha.example",
		IsNotResponding: true, NotRespondingSince: &since,
	}
	require.NoError(t, svc.RecordResponseSuccess("alpha.example"))
	assert.False(t, repo.Instances["alpha.example"].IsNotResponding)
}

func TestService_RecordResponseSuccess_UnknownHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.RecordResponseSuccess("missing.example"))
}

func TestService_RecordResponseSuccess_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	require.NoError(t, svc.RecordResponseSuccess(""))
}

func TestService_IsBlocked(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.BlockedHosts = pq.StringArray{"bad.example"}
	assert.True(t, svc.IsBlocked("bad.example"))
	assert.False(t, svc.IsBlocked("good.example"))
	assert.False(t, svc.IsBlocked(""))
}

func TestService_IsBlocked_MetaError(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta = nil
	assert.False(t, svc.IsBlocked("any.example"))
}

func TestService_IsSilenced(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.SilencedHosts = pq.StringArray{"quiet.example"}
	assert.True(t, svc.IsSilenced("quiet.example"))
	assert.False(t, svc.IsSilenced("loud.example"))
	assert.False(t, svc.IsSilenced(""))
}

func TestService_IsSilenced_MetaError(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta = nil
	assert.False(t, svc.IsSilenced("any.example"))
}

func TestService_FederationHostLists(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.BlockedHosts = pq.StringArray{"bad.example"}
	metaRepo.Meta.SilencedHosts = pq.StringArray{"quiet.example"}
	metaRepo.Meta.MediaSilencedHosts = pq.StringArray{"media.example"}
	hosts, err := svc.FederationHostLists()
	require.NoError(t, err)
	assert.Equal(t, []string{"bad.example"}, []string(hosts.Blocked))
	assert.Equal(t, []string{"quiet.example"}, []string(hosts.Silenced))
	assert.Equal(t, []string{"media.example"}, []string(hosts.MediaSilenced))
}

// meta が読めない場合は error を返す (admin endpoint なので握り潰さない)。
func TestService_FederationHostLists_MetaError(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta = nil
	_, err := svc.FederationHostLists()
	assert.Error(t, err)
}

// federation == "all" (default) は blockedHosts 以外の全 host を allow する。
func TestService_IsAllowed_AllMode(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.Federation = "all"
	metaRepo.Meta.BlockedHosts = pq.StringArray{"bad.example"}
	assert.True(t, svc.IsAllowed("good.example"))
	assert.False(t, svc.IsAllowed("bad.example"))
	assert.True(t, svc.IsAllowed(""), "empty host (= local) is always allowed")
}

// federation == "specified" は federationHosts に列挙された host だけ allow。
func TestService_IsAllowed_SpecifiedMode(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.Federation = "specified"
	metaRepo.Meta.FederationHosts = pq.StringArray{"allowed.example"}
	assert.True(t, svc.IsAllowed("allowed.example"))
	assert.False(t, svc.IsAllowed("other.example"))
}

// specified モードでも blockedHosts に入っていれば deny される (allowlist と
// blocklist の AND)。
func TestService_IsAllowed_SpecifiedRespectsBlock(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.Federation = "specified"
	metaRepo.Meta.FederationHosts = pq.StringArray{"allowed.example"}
	metaRepo.Meta.BlockedHosts = pq.StringArray{"allowed.example"}
	assert.False(t, svc.IsAllowed("allowed.example"),
		"a host listed in both federationHosts and blockedHosts must be denied")
}

// federation == "none" は全 host を deny する。
func TestService_IsAllowed_NoneMode(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta.Federation = "none"
	metaRepo.Meta.FederationHosts = pq.StringArray{"allowed.example"}
	assert.False(t, svc.IsAllowed("allowed.example"))
	assert.False(t, svc.IsAllowed("other.example"))
	assert.True(t, svc.IsAllowed(""), "empty host (= local) is always allowed even in none mode")
}

// meta が読めないときは安全側ではなく allow を返す (transient error で
// 連合全体が落ちる事故を避ける)。
func TestService_IsAllowed_MetaError(t *testing.T) {
	svc, _, metaRepo := newService(t)
	metaRepo.Meta = nil
	assert.True(t, svc.IsAllowed("any.example"))
}

// host とリスト要素の比較は Misskey TS と同じ case-insensitive な suffix
// match で行う (#590)。下記マトリクスでは IsAllowed (specified モード) /
// IsBlocked / IsSilenced すべてが同じ helper を共有している前提を確認する。
func TestService_HostMatching(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{name: "exact match", pattern: "example.com", host: "example.com", want: true},
		{name: "subdomain match", pattern: "example.com", host: "sub.example.com", want: true},
		{name: "deep subdomain match", pattern: "example.com", host: "a.b.c.example.com", want: true},
		{name: "case-insensitive host", pattern: "example.com", host: "Example.Com", want: true},
		{name: "case-insensitive pattern", pattern: "Example.Com", host: "example.com", want: true},
		{name: "non-matching host", pattern: "example.com", host: "other.com", want: false},
		// 部分一致のように見えるが TS と同じく "." prefix 比較なので
		// 別ドメインとして弾く。e.g. evilexample.com は example.com に含まれない。
		{name: "non-matching prefix similar host", pattern: "example.com", host: "evilexample.com", want: false},
		// pattern が "." を含まない 1 階層 TLD-like なケース。`x` が
		// `bar.x` / `baz.x` / `x` のいずれにもマッチする (TS と同じ)。
		{name: "single-label pattern matches subdomains", pattern: "x", host: "bar.x", want: true},
		{name: "single-label pattern exact match", pattern: "x", host: "x", want: true},
		{name: "single-label pattern non-match", pattern: "x", host: "y", want: false},
		// 空 pattern は意図的に skip する (host="" guard が既に効くので
		// "." 単独で任意 host にマッチする実害は無いが、admin が誤って
		// 空エントリを混入させたとき "全 host allow/block" に化けないよう
		// defensive guard)。
		{name: "empty pattern never matches", pattern: "", host: "example.com", want: false},
		// IDN raw 表記 (Punycode 化前) でも case-insensitive で一致すること。
		// strings.ToLower は ASCII 専用だが HostMatchesAny は Unicode-aware
		// な x/text/cases.Caser を通しているので、TS の String.toLowerCase()
		// と同じ挙動を保てる (#590 review #5)。
		{name: "unicode case-insensitive (German)", pattern: "müNSTer.example", host: "MÜNSTER.example", want: true},
		{name: "unicode subdomain match", pattern: "müNSTer.example", host: "sub.münster.example", want: true},
	}
	for _, tc := range cases {
		t.Run("IsAllowed_specified/"+tc.name, func(t *testing.T) {
			svc, _, metaRepo := newService(t)
			metaRepo.Meta.Federation = "specified"
			metaRepo.Meta.FederationHosts = pq.StringArray{tc.pattern}
			assert.Equal(t, tc.want, svc.IsAllowed(tc.host))
		})
		t.Run("IsBlocked/"+tc.name, func(t *testing.T) {
			svc, _, metaRepo := newService(t)
			metaRepo.Meta.BlockedHosts = pq.StringArray{tc.pattern}
			assert.Equal(t, tc.want, svc.IsBlocked(tc.host))
		})
		t.Run("IsSilenced/"+tc.name, func(t *testing.T) {
			svc, _, metaRepo := newService(t)
			metaRepo.Meta.SilencedHosts = pq.StringArray{tc.pattern}
			assert.Equal(t, tc.want, svc.IsSilenced(tc.host))
		})
	}
}

func TestService_Suspend(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	require.NoError(t, svc.Suspend("alpha.example", model.SuspensionStateManuallySuspended))
	assert.Equal(t, model.SuspensionStateManuallySuspended, repo.Instances["alpha.example"].SuspensionState)
}

func TestService_Suspend_NotFound(t *testing.T) {
	svc, _, _ := newService(t)
	err := svc.Suspend("missing.example", model.SuspensionStateManuallySuspended)
	assert.ErrorIs(t, err, instance.ErrInstanceNotFound)
}

func TestService_Suspend_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	err := svc.Suspend("", model.SuspensionStateManuallySuspended)
	assert.Error(t, err)
}

func TestService_UpdateModerationNote(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	require.NoError(t, svc.UpdateModerationNote("alpha.example", "spam"))
	assert.Equal(t, "spam", repo.Instances["alpha.example"].ModerationNote)
}

func TestService_UpdateModerationNote_NotFound(t *testing.T) {
	svc, _, _ := newService(t)
	err := svc.UpdateModerationNote("missing.example", "x")
	assert.ErrorIs(t, err, instance.ErrInstanceNotFound)
}

func TestService_UpdateModerationNote_EmptyHost(t *testing.T) {
	svc, _, _ := newService(t)
	err := svc.UpdateModerationNote("", "x")
	assert.Error(t, err)
}

func TestService_FindByHost(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	got, err := svc.FindByHost("alpha.example")
	require.NoError(t, err)
	assert.Equal(t, "i1", got.ID)
}

func TestService_FindByHost_NotFound(t *testing.T) {
	svc, _, _ := newService(t)
	_, err := svc.FindByHost("missing.example")
	assert.ErrorIs(t, err, instance.ErrInstanceNotFound)
}

// TestService_FindByHost_DBError は #915 review fix の regression guard。
// repo が gorm.ErrRecordNotFound 以外の error を返した場合、Service は raw
// err を保ち ErrInstanceNotFound に潰さないこと。観測性 (= 500 + alert)
// を確保するため。
func TestService_FindByHost_DBError(t *testing.T) {
	svc, repo, _ := newService(t)
	dbErr := errors.New("connection reset by peer")
	repo.FindByHostErr = dbErr
	_, err := svc.FindByHost("alpha.example")
	require.Error(t, err)
	assert.NotErrorIs(t, err, instance.ErrInstanceNotFound)
	assert.ErrorIs(t, err, dbErr)
}

func TestService_List(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	repo.Instances["beta.example"] = &model.Instance{ID: "i2", Host: "beta.example"}
	rows, err := svc.List(model.InstanceListFilter{})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// stubMetadataFetcher records the hosts it was asked to fetch.
type stubMetadataFetcher struct {
	hosts []string
	err   error
}

func (s *stubMetadataFetcher) Fetch(host string) error {
	s.hosts = append(s.hosts, host)
	return s.err
}

func TestService_RegisterFromHost_TriggersMetadataFetch(t *testing.T) {
	svc, _, _ := newService(t)
	fetcher := &stubMetadataFetcher{}
	svc.SetMetadataFetcher(fetcher)
	_, err := svc.RegisterFromHost("alpha.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha.example"}, fetcher.hosts)
}

func TestService_RegisterFromHost_MetadataFetcherErrorIgnored(t *testing.T) {
	svc, _, _ := newService(t)
	fetcher := &stubMetadataFetcher{err: errors.New("net down")}
	svc.SetMetadataFetcher(fetcher)
	_, err := svc.RegisterFromHost("alpha.example")
	// fetcher エラーは握りつぶされる
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha.example"}, fetcher.hosts)
}

func TestService_RegisterFromHost_NoFetchOnExisting(t *testing.T) {
	svc, repo, _ := newService(t)
	repo.Instances["alpha.example"] = &model.Instance{ID: "i1", Host: "alpha.example"}
	fetcher := &stubMetadataFetcher{}
	svc.SetMetadataFetcher(fetcher)
	_, err := svc.RegisterFromHost("alpha.example")
	require.NoError(t, err)
	// 既存行に対しては fetch しない
	assert.Empty(t, fetcher.hosts)
}

// FetchMetadataService が MetadataFetcher interface を実装していることを確認。
// 配線時に router.go でこの代入が成立する必要がある。
func TestFetchMetadataService_ImplementsMetadataFetcher(t *testing.T) {
	var _ instance.MetadataFetcher = (*instance.FetchMetadataService)(nil)
}

func TestService_SetClock(t *testing.T) {
	svc, repo, _ := newService(t)
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	svc.SetClock(nil) // nil 渡しは無視
	got, err := svc.RegisterFromHost("alpha.example")
	require.NoError(t, err)
	assert.Equal(t, fixed, got.FirstRetrievedAt)
	assert.Equal(t, fixed, repo.Instances["alpha.example"].FirstRetrievedAt)
}
