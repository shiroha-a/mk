package admin

import (
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admin/show-user の roles は *model.Role をそのまま JSON 化するため iconUrl が
// 生で出ていた。軽量ヘルパーで iconUrl だけ media proxy 経由へ差し替える
// (#1529)。PackRole に寄せないのは usersCount (per-role COUNT) を増やさないため。
func TestProxyRoleIconURLs(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", []byte("s"), false, true))
	t.Cleanup(func() { entity.SetMediaURLContext(nil) })

	remote := "https://remote.example/r.png"
	local := "https://mk.example/files/r.png"
	origRemote := &model.Role{ID: "r1", Name: "remote", IconURL: &remote}
	origLocal := &model.Role{ID: "r2", Name: "local", IconURL: &local}

	out := proxyRoleIconURLs([]*model.Role{origRemote, nil, origLocal})
	require.Len(t, out, 3)
	assert.Nil(t, out[1], "nil element はそのまま (呼び出し元の shape を変えない)")

	iconURL0, _ := out[0]["iconUrl"].(*string)
	require.NotNil(t, iconURL0)
	assert.True(t, strings.HasPrefix(*iconURL0, "https://mk.example/proxy/image.webp?"),
		"remote role icon must be proxied, got %q", *iconURL0)
	iconURL2, _ := out[2]["iconUrl"].(*string)
	require.NotNil(t, iconURL2)
	assert.Equal(t, local, *iconURL2, "自オリジンは no-op")

	// **元の model.Role は書き換えない。** GetUserRoles の返り値を共有している
	// 呼び出し元 (キャッシュ等) に副作用を出さないため。
	assert.Equal(t, remote, *origRemote.IconURL)
	assert.Equal(t, local, *origLocal.IconURL)

	// iconUrl 以外の field は保持する (shape drift には触らない。json
	// round-trip 経由なので id/name も json タグどおりの key で入っている)。
	assert.Equal(t, "remote", out[0]["name"])
	assert.Equal(t, "r2", out[2]["id"])
}
