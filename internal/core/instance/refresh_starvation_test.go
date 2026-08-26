package instance_test

import (
	"errors"
	"testing"

	"github.com/shiroha-a/mk/internal/core/instance"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeinfo を取れない host でも `infoUpdatedAt` を進める (#2730)。
//
// 進めないと `ListForRefresh` の `ORDER BY "infoUpdatedAt" ASC NULLS FIRST` で
// 先頭を占め続け、`BatchLimit` (既定 100) を食い潰して他の host が永久に
// refresh されなくなる。**候補から抜ける経路が事実上無い host がいる** —
// `isNotResponding` は AP 配送の失敗でしか立たず、`MarkRequestReceived` が
// inbound で false に戻すため。
func TestFetch_AdvancesInfoUpdatedAtEvenWhenNodeinfoFails(t *testing.T) {
	const disc1_0 = `{"links":[{"rel":"http://nodeinfo.diaspora.software/ns/schema/1.0",` +
		`"href":"https://remote.example/nodeinfo/1.0"}]}`

	cases := []struct {
		name   string
		bodies [][]byte
		errs   []error
		// wantErr は「nodeinfo として読めなかった」= 呼び出し元へ error を返す。
		wantErr bool
	}{
		{"discovery が 404", [][]byte{nil}, []error{errors.New("404")}, true},
		{"links が空", [][]byte{[]byte(`{"links":[]}`)}, nil, true},
		{"未知の rel しか無い", [][]byte{[]byte(`{"links":[{"rel":"x","href":"y"}]}`)}, nil, true},
		{"nodeinfo が壊れた JSON", [][]byte{[]byte(discoveryBody), []byte("{not json")}, nil, true},
		{"nodeinfo の取得が失敗", [][]byte{[]byte(discoveryBody), nil}, []error{nil, errors.New("doc fail")}, true},
		// 下 2 つは upstream では成功する。mk-go も成功させる。
		{"1.0 の link しか無い", [][]byte{[]byte(disc1_0), []byte(documentBody)}, nil, false},
		{"object でない JSON", [][]byte{[]byte(discoveryBody), []byte(`[]`)}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewMockInstanceRepository()
			repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
			fetcher := &scriptedFetcher{bodies: tc.bodies, errs: tc.errs}
			err := instance.NewFetchMetadataService(repo, fetcher).Fetch("remote.example")
			if tc.wantErr {
				// **error は返す。** 返さないと instance_refresh の warn が消えて
				// 壊れた host が見えなくなる。
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			// 成否に関わらず infoUpdatedAt は進む。
			assert.NotNil(t, repo.Instances["remote.example"].InfoUpdatedAt,
				"infoUpdatedAt が進まないと ListForRefresh の先頭に居座る")
		})
	}
}

// nodeinfo を取れなくても icon / favicon は書く (#2730)。upstream も
// `Promise.all([fetchNodeinfo, fetchDom, fetchManifest])` で並べている。
func TestFetch_WritesIconsWhenNodeinfoFails(t *testing.T) {
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{
		bodies:   [][]byte{nil},
		errs:     []error{errors.New("nodeinfo 404")},
		htmlBody: []byte(`<html><head><link rel="icon" href="/favicon.png"></head></html>`),
	}
	require.Error(t, instance.NewFetchMetadataService(repo, fetcher).Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.FaviconURL)
	assert.Equal(t, "https://remote.example/favicon.png", *got.FaviconURL)
	require.NotNil(t, got.IconURL)
	assert.Equal(t, "https://remote.example/favicon.png", *got.IconURL)
}

// nodeinfo 1.0 しか出さない host からもメタ情報を取れる (#2730)。
// upstream の `link2_1 ?? link2_0 ?? link1_0` に揃える。
func TestFetch_FallsBackToNodeinfo10(t *testing.T) {
	const disc = `{"links":[{"rel":"http://nodeinfo.diaspora.software/ns/schema/1.0",` +
		`"href":"https://remote.example/nodeinfo/1.0"}]}`
	// nodeinfo 1.0 の実際の形。protocols の shape は 2.0 と違うが、読む field は同じ。
	const body = `{
		"version": "1.0",
		"software": {"name": "Friendica", "version": "2024.03"},
		"protocols": {"inbound": ["activitypub"], "outbound": ["activitypub"]},
		"openRegistrations": false,
		"metadata": {"nodeName": "Remote 1.0", "nodeDescription": "old schema"}
	}`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	fetcher := &scriptedFetcher{bodies: [][]byte{[]byte(disc), []byte(body)}}
	require.NoError(t, instance.NewFetchMetadataService(repo, fetcher).Fetch("remote.example"))

	got := repo.Instances["remote.example"]
	require.NotNil(t, got.SoftwareName)
	assert.Equal(t, "friendica", *got.SoftwareName)
	require.NotNil(t, got.SoftwareVersion)
	assert.Equal(t, "2024.03", *got.SoftwareVersion)
	require.NotNil(t, got.Name)
	assert.Equal(t, "Remote 1.0", *got.Name)
	require.NotNil(t, got.OpenRegistrations)
	assert.False(t, *got.OpenRegistrations)
}

// 2.1 / 2.0 があれば 1.0 より優先する (upstream の `??` の順序)。
func TestFetch_PrefersNewerSchemaOverNodeinfo10(t *testing.T) {
	const disc = `{"links":[
		{"rel":"http://nodeinfo.diaspora.software/ns/schema/1.0","href":"https://remote.example/nodeinfo/1.0"},
		{"rel":"http://nodeinfo.diaspora.software/ns/schema/2.0","href":"https://remote.example/nodeinfo/2.0"}
	]}`
	repo := testutil.NewMockInstanceRepository()
	repo.Instances["remote.example"] = &model.Instance{ID: "i1", Host: "remote.example"}
	// 2 番目の body (= 選ばれた href の応答) が 2.0 のものになる。
	fetcher := &scriptedFetcher{bodies: [][]byte{[]byte(disc), []byte(documentBody)}}
	require.NoError(t, instance.NewFetchMetadataService(repo, fetcher).Fetch("remote.example"))
	assert.Equal(t, "https://remote.example/nodeinfo/2.0", fetcher.jsonURLs[1])
}

// object でない JSON は upstream と同じく `'?'` になる (#2730)。error にすると
// infoUpdatedAt が書けず starvation の原因になる。
func TestFetch_NonObjectNodeinfoStoresPlaceholder(t *testing.T) {
	for _, body := range []string{`[]`, `123`, `"x"`, `true`} {
		t.Run(body, func(t *testing.T) {
			got := fetchNodeinfo(t, body)
			require.NotNil(t, got.SoftwareName)
			assert.Equal(t, "?", *got.SoftwareName)
			assert.NotNil(t, got.InfoUpdatedAt)
		})
	}
}

// JS で falsy な値は upstream の `if (info)` を通らないので、`'?'` も書かない。
func TestFetch_FalsyNodeinfoWritesNoSoftwareName(t *testing.T) {
	for _, body := range []string{`null`, `false`, `0`, `""`} {
		t.Run(body, func(t *testing.T) {
			repo := testutil.NewMockInstanceRepository()
			existing := "misskey"
			repo.Instances["remote.example"] = &model.Instance{
				ID: "i1", Host: "remote.example", SoftwareName: &existing,
			}
			fetcher := &scriptedFetcher{bodies: [][]byte{[]byte(discoveryBody), []byte(body)}}
			require.NoError(t, instance.NewFetchMetadataService(repo, fetcher).Fetch("remote.example"))

			got := repo.Instances["remote.example"]
			require.NotNil(t, got.SoftwareName)
			assert.Equal(t, "misskey", *got.SoftwareName, "既存値を `?` で上書きしない")
			assert.NotNil(t, got.InfoUpdatedAt)
		})
	}
}
