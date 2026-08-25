package federation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostFromURI は保存形の host を作る**唯一の入口**なので、ここで正規化する
// (#2706)。`url.Parse` は小文字化も punycode 化もしないため、正規化しないと
// `Mixed.Example` のような表記の行が入り、連合ゲートや timeline の instance-mute
// (完全一致) が取りこぼす。
func TestHostFromURI_Normalizes(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"already normalized", "https://remote.example/users/x", "remote.example"},
		{"mixed case is lowercased", "https://Mixed.Example/users/x", "mixed.example"},
		{"unicode IDN becomes punycode", "https://パイ.example/users/x", "xn--eckve.example"},
		{"punycode is kept", "https://xn--eckve.example/users/x", "xn--eckve.example"},
		{"port is kept", "https://Mixed.Example:8443/users/x", "mixed.example:8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostFromURI(tc.uri)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// host が無い / 壊れた URI は従来どおりエラー。
func TestHostFromURI_Rejects(t *testing.T) {
	for _, uri := range []string{"", "/users/x", "not a uri\x7f"} {
		_, err := hostFromURI(uri)
		assert.Error(t, err, uri)
	}
}

// 保存形と比較用が同じ値を作ること。ずれると、正規化して保存した行を
// 読み取り側が引けなくなる。
func TestHostFromURI_MatchesPunyHost(t *testing.T) {
	for _, uri := range []string{
		"https://Mixed.Example/users/x",
		"https://パイ.example/users/x",
		"https://remote.example/users/x",
	} {
		got, err := hostFromURI(uri)
		require.NoError(t, err)
		assert.Equal(t, punyHost(got), got, uri)
	}
}

// port だけで host が無い URI は弾くこと。`u.Host` を見ていると `":8443"` が
// 通ってしまう (#2714 review LOW-9)。
func TestHostFromURI_RejectsPortWithoutHost(t *testing.T) {
	_, err := hostFromURI("https://:8443/users/x")
	assert.Error(t, err)
}
