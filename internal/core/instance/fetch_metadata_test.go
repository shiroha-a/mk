package instance_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedFetcher returns canned responses keyed by call order.
// FetchObjectUnsigned / FetchHTML それぞれ独立カウンタで bodies / errs を
// 消費する。
type scriptedFetcher struct {
	bodies [][]byte
	errs   []error
	idx    int

	// HTMLレスポンスは nodeinfo fetch と独立に供給する。未設定なら
	// FetchHTML 呼出は error を返して icon 抽出 step を no-op にする
	// (nodeinfo テストを icon logic の追加で壊さないため)。
	htmlBody []byte
	htmlErr  error
}

func (s *scriptedFetcher) FetchJSON(_ string) ([]byte, error) {
	if s.idx >= len(s.bodies) {
		return nil, errors.New("no more bodies")
	}
	b := s.bodies[s.idx]
	var err error
	if s.idx < len(s.errs) {
		err = s.errs[s.idx]
	}
	s.idx++
	return b, err
}

func (s *scriptedFetcher) FetchHTML(_ string) ([]byte, error) {
	if s.htmlErr != nil {
		return nil, s.htmlErr
	}
	if s.htmlBody == nil {
		return nil, errors.New("no html body")
	}
	return s.htmlBody, nil
}

func newFetchSvc(t *testing.T, bodies [][]byte, errs []error) (*instance.FetchMetadataService, *testutil.MockInstanceRepository) {
	t.Helper()
	repo := testutil.NewMockInstanceRepository()
	fetcher := &scriptedFetcher{bodies: bodies, errs: errs}
	return instance.NewFetchMetadataService(repo, fetcher), repo
}

const discoveryBody = `{
	"links": [
		{"rel": "http://nodeinfo.diaspora.software/ns/schema/2.0", "href": "https://remote.example/nodeinfo/2.0"},
		{"rel": "http://nodeinfo.diaspora.software/ns/schema/2.1", "href": "https://remote.example/nodeinfo/2.1"}
	]
}`

const documentBody = `{
	"software": {"name": "misskey", "version": "13.14.2"},
	"openRegistrations": true,
	"metadata": {
		"nodeName": "Remote",
		"nodeDescription": "A test instance",
		"themeColor": "#abcdef"
	}
}`

func TestFetch_HappyPath(t *testing.T) {
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), []byte(documentBody)}, nil)
	repo.Instances["remote.example"] = &model.Instance{
		ID: "i1", Host: "remote.example",
	}
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.SoftwareName)
	assert.Equal(t, "misskey", *got.SoftwareName)
	require.NotNil(t, got.SoftwareVersion)
	assert.Equal(t, "13.14.2", *got.SoftwareVersion)
	require.NotNil(t, got.OpenRegistrations)
	assert.True(t, *got.OpenRegistrations)
	require.NotNil(t, got.Name)
	assert.Equal(t, "Remote", *got.Name)
	require.NotNil(t, got.Description)
	require.NotNil(t, got.ThemeColor)
	require.NotNil(t, got.InfoUpdatedAt)
}

func TestFetch_EmptyHost(t *testing.T) {
	svc, _ := newFetchSvc(t, nil, nil)
	err := svc.Fetch("")
	assert.Error(t, err)
}

func TestFetch_InstanceNotFound(t *testing.T) {
	svc, _ := newFetchSvc(t, nil, nil)
	err := svc.Fetch("missing.example")
	assert.ErrorIs(t, err, instance.ErrInstanceNotFound)
}

func TestFetch_DiscoveryError(t *testing.T) {
	svc, repo := newFetchSvc(t, [][]byte{nil}, []error{errors.New("net down")})
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	err := svc.Fetch("remote.example")
	assert.Error(t, err)
}

func TestFetch_DiscoveryBadJSON(t *testing.T) {
	svc, repo := newFetchSvc(t, [][]byte{[]byte("{not json")}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	err := svc.Fetch("remote.example")
	assert.Error(t, err)
}

func TestFetch_NoSupportedSchema(t *testing.T) {
	svc, repo := newFetchSvc(t, [][]byte{[]byte(`{"links":[{"rel":"x","href":"y"}]}`)}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	err := svc.Fetch("remote.example")
	assert.Error(t, err)
}

func TestFetch_DocumentError(t *testing.T) {
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), nil},
		[]error{nil, errors.New("doc fail")})
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	err := svc.Fetch("remote.example")
	assert.Error(t, err)
}

func TestFetch_DocumentBadJSON(t *testing.T) {
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), []byte("{not json")}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	err := svc.Fetch("remote.example")
	assert.Error(t, err)
}

func TestFetch_OnlyVersion20(t *testing.T) {
	disc := `{"links":[{"rel":"http://nodeinfo.diaspora.software/ns/schema/2.0","href":"https://remote.example/nodeinfo/2.0"}]}`
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(disc), []byte(documentBody)}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	require.NoError(t, svc.Fetch("remote.example"))
}

func TestFetch_DocumentMinimalFields(t *testing.T) {
	doc := `{"software":{"name":"misskey","version":""},"metadata":{}}`
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), []byte(doc)}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	require.NoError(t, svc.Fetch("remote.example"))
	got := repo.Instances["remote.example"]
	require.NotNil(t, got.SoftwareName)
	assert.Nil(t, got.SoftwareVersion)
	assert.Nil(t, got.Name)
	assert.Nil(t, got.Description)
	assert.Nil(t, got.ThemeColor)
}

func TestFetch_IconFromHTML(t *testing.T) {
	htmlBody := `<html><head>
		<link rel="icon" href="/favicon-16.png">
		<link rel="apple-touch-icon" href="https://cdn.example/apple-touch.png">
	</head></html>`
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), []byte(documentBody)}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	// scriptedFetcherに直接htmlBody仕込む。 fetcher取得はnewFetchSvc内部なので
	// この test では直接 service を作り直す必要がある。
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlBody: []byte(htmlBody),
	}
	svc = instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.IconURL)
	// iconUrl: 高解像度の apple-touch-icon が優先される。
	assert.Equal(t, "https://cdn.example/apple-touch.png", *got.IconURL)
	require.NotNil(t, got.FaviconURL)
	// faviconUrl (#474): HTML の <link rel="icon"> URL を優先するように
	// 修正。Iceshrimp.NET 等が `/favicon.ico` ではなく `/favicon.png` を
	// 提供する場合に、404 を返す hardcode URL ではなく実 URL が保存される。
	assert.Equal(t, "https://remote.example/favicon-16.png", *got.FaviconURL)
}

// TestFetch_IconFromHTML_RelIconOnly_UsesItForFavicon: Iceshrimp.NET 風の
// HTML (apple-touch-icon は無く `<link rel="icon">` のみ) では、両方の
// field が同じ icon URL を指す。これが #474 の本丸。
func TestFetch_IconFromHTML_RelIconOnly_UsesItForFavicon(t *testing.T) {
	// Iceshrimp.NET と同じパターン: icon のみで apple-touch-icon は無い
	htmlBody := `<html><head>
		<link rel="icon" type="image/png" href="/favicon.png">
	</head></html>`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlBody: []byte(htmlBody),
	}
	svc := instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.IconURL)
	assert.Equal(t, "https://remote.example/favicon.png", *got.IconURL)
	require.NotNil(t, got.FaviconURL)
	// 旧実装では `/favicon.ico` 決め打ち → 404 → frontend で表示できない。
	// 修正後は HTML の <link rel="icon"> が両 field に使われる。
	assert.Equal(t, "https://remote.example/favicon.png", *got.FaviconURL)
}

func TestFetch_IconFromHTML_RelativeResolved(t *testing.T) {
	htmlBody := `<html><head><link rel="shortcut icon" href="/static/icon.svg"></head></html>`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlBody: []byte(htmlBody),
	}
	svc := instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.IconURL)
	// 相対パスは pageURL (https://remote.example/) を基準に絶対化される。
	assert.Equal(t, "https://remote.example/static/icon.svg", *got.IconURL)
}

func TestFetch_IconFromHTML_NoLinkTag(t *testing.T) {
	htmlBody := `<html><head><title>no icon</title></head></html>`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlBody: []byte(htmlBody),
	}
	svc := instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	// iconUrl は抽出できないので nilのまま。
	assert.Nil(t, got.IconURL)
	// faviconUrl は HTML取得成功時でもconventionalな /favicon.ico で上書きされる。
	require.NotNil(t, got.FaviconURL)
	assert.Equal(t, "https://remote.example/favicon.ico", *got.FaviconURL)
}

func TestFetch_IconURLExceedsColumnLimit(t *testing.T) {
	// 256文字制約を超えるCDN URL。この場合iconUrlは書き込まずに
	// nodeinfo だけ UpdateFields できるはず (Devin BUG 指摘の回帰防止)。
	longPath := strings.Repeat("a", 300)
	htmlBody := `<html><head><link rel="icon" href="https://cdn.example/` + longPath + `.png"></head></html>`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlBody: []byte(htmlBody),
	}
	svc := instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	// 長すぎるicon URLは保存されない
	assert.Nil(t, got.IconURL)
	// favicon (https://remote.example/favicon.ico = 34文字) は保存される
	require.NotNil(t, got.FaviconURL)
	// nodeinfo fields は失われない
	require.NotNil(t, got.SoftwareName)
	assert.Equal(t, "misskey", *got.SoftwareName)
}

func TestFetch_IconHTMLFetchError(t *testing.T) {
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:  [][]byte{[]byte(discoveryBody), []byte(documentBody)},
		htmlErr: errors.New("conn refused"),
	}
	svc := instance.NewFetchMetadataService(repo, fetcher)
	require.NoError(t, svc.Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	// HTMLが取れなくてもfaviconは決め打ちで保存される。
	assert.Nil(t, got.IconURL)
	require.NotNil(t, got.FaviconURL)
	assert.Equal(t, "https://remote.example/favicon.ico", *got.FaviconURL)
}

func TestFetch_SetClock(t *testing.T) {
	svc, repo := newFetchSvc(t,
		[][]byte{[]byte(discoveryBody), []byte(documentBody)}, nil)
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return fixed })
	svc.SetClock(nil) // nil 渡し無視
	require.NoError(t, svc.Fetch("remote.example"))
	require.NotNil(t, repo.Instances["remote.example"].InfoUpdatedAt)
	assert.Equal(t, fixed, *repo.Instances["remote.example"].InfoUpdatedAt)
}
