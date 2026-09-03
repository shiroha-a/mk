package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/plugin"
)

// 宣言と運営者の設定から実効上限を決める。**宣言だけでは既定値を超えられない**
// のが要点で、ここが緩むとプラグインを 1 つ足すだけで露出が広がる。
func TestPeerBodyLimit(t *testing.T) {
	tests := []struct {
		name     string
		declared int64
		settings map[string]any
		want     int64
		clamped  bool
	}{
		{name: "unset falls back to the default", want: peerDefaultMaxBody},
		{name: "declaration below the default is honored", declared: 8 << 10, want: 8 << 10},
		{name: "declaration at the default is honored", declared: peerDefaultMaxBody, want: peerDefaultMaxBody},
		{
			name:     "declaration above the default is clamped without an override",
			declared: 512 << 10,
			want:     peerDefaultMaxBody,
			clamped:  true,
		},
		{
			name:     "operator override lifts the declaration",
			declared: 512 << 10,
			settings: map[string]any{peerMaxBodyKey: 512 << 10},
			want:     512 << 10,
		},
		{
			name:     "operator override is capped by the hard maximum",
			settings: map[string]any{peerMaxBodyKey: 64 << 20},
			want:     peerHardMaxBody,
		},
		{
			name:     "operator override may lower the declaration",
			declared: peerDefaultMaxBody,
			settings: map[string]any{peerMaxBodyKey: 2 << 10},
			want:     2 << 10,
		},
		{
			name:     "override below the floor is raised",
			settings: map[string]any{peerMaxBodyKey: 1},
			want:     peerMinMaxBody,
		},
		{
			name:     "float override (viper YAML) is accepted",
			settings: map[string]any{peerMaxBodyKey: float64(8 << 10)},
			want:     8 << 10,
		},
		{
			name:     "non-numeric override is ignored",
			declared: 8 << 10,
			settings: map[string]any{peerMaxBodyKey: "big"},
			want:     8 << 10,
		},
		{
			name:     "non-positive override is ignored",
			declared: 8 << 10,
			settings: map[string]any{peerMaxBodyKey: 0},
			want:     8 << 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, clamped := peerBodyLimit(plugin.Definition{Name: "demo", PeerMaxBody: tt.declared}, tt.settings)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.clamped, clamped)
		})
	}
}

// BodyLimitByPath へ渡す表は、peered かつ有効なプラグインだけを載せる。
func TestPeerBodyLimitsByPath(t *testing.T) {
	defs := []plugin.Definition{
		{Name: "small", Peered: true, PeerMaxBody: 4 << 10},
		{Name: "plain", Peered: false, PeerMaxBody: 0},
		{Name: "off", Peered: true, PeerMaxBody: 4 << 10},
		{Name: "dflt", Peered: true},
	}
	got := peerBodyLimitsByPath(defs, map[string]map[string]any{
		"off": {enabledKey: false},
	})

	assert.Equal(t, map[string]int64{
		"/api/plugin/small/_peer": 4 << 10,
		"/api/plugin/dflt/_peer":  peerDefaultMaxBody,
	}, got)

	assert.Nil(t, peerBodyLimitsByPath(nil, nil))
	assert.Nil(t, peerBodyLimitsByPath([]plugin.Definition{{Name: "plain"}}, nil), "peered が無ければ表を作らない")
}

// 送信側は **エンベロープで測る**。payload だけで測ると相関 ID の分だけ境界が
// ずれ、上限ちょうどの payload がここを通って相手で 413 になる。
func TestPluginPeer_SendMeasuresEnvelope(t *testing.T) {
	const limit int64 = 1 << 10

	var got int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = int64(len(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	key, _ := testPeerKeypair(t)
	gen := mustGenerator(t)
	p := testPeer(t, &pluginPeerDeps{
		client: srv.Client(),
		signer: &fakePeerSigner{key: key},
		remote: &fakePeerLister{byHost: map[string][]string{"other.example": {"demo"}}},
		idGen:  gen,
		urlFor: func(_, plugin string) string { return srv.URL + peerAPIPrefix + plugin + peerPath },
	})
	p.maxBody = limit

	// エンベロープ = {"id":"<aidx>","payload":"<...>"}。payload の JSON 表現が
	// ちょうど上限を埋める長さを作る。
	overhead := int64(len(`{"id":"","payload":}`) + len(gen.Generate(time.Now())))
	body := strings.Repeat("x", int(limit-overhead-2)) // 引用符 2 つ分を引く

	_, err := p.Send(context.Background(), "other.example", body+"xx")
	require.Error(t, err, "上限を 2 バイト超えると送らない")
	assert.Contains(t, err.Error(), "大きすぎます")

	sendID, err := p.Send(context.Background(), "other.example", body)
	require.NoError(t, err, "上限ちょうどは通る (境界を見ていることの確認)")
	require.NotEmpty(t, sendID)

	require.Eventually(t, func() bool { return got != 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, limit, got, "実際に飛んだ本文が上限ちょうど")
}

// 上限を超える応答は **切り詰めずにエラーにする**。黙って先頭だけ渡すと
// OnReply が途中で切れた JSON を受け取る。
func TestPluginPeer_RejectsOversizedReply(t *testing.T) {
	const limit int64 = 1 << 10
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"v":"` + strings.Repeat("y", int(limit)) + `"}`))
	}))
	defer srv.Close()

	key, _ := testPeerKeypair(t)
	p := testPeer(t, &pluginPeerDeps{
		client: srv.Client(),
		signer: &fakePeerSigner{key: key},
		urlFor: func(_, plugin string) string { return srv.URL + peerAPIPrefix + plugin + peerPath },
	})
	p.maxBody = limit

	called := make(chan struct{}, 1)
	p.OnReply(func(context.Context, string, string, json.RawMessage) error {
		called <- struct{}{}
		return nil
	})

	_, err := p.post(srv.URL+peerAPIPrefix+"demo"+peerPath, []byte(`{"id":"i","payload":1}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "応答が大きすぎます")

	select {
	case <-called:
		t.Fatal("切り詰めた応答を OnReply に渡してはいけない")
	default:
	}
}

func mustGenerator(t *testing.T) id.Generator {
	t.Helper()
	g, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	return g
}
