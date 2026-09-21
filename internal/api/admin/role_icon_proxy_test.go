package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
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

// round-trip が JSON の shape (キー集合) を保つこと自体は #3130 review 3周目
// まで未固定だった。`json.Unmarshal` 前後で model.Role の全 field が生き残る
// ことを、field 名を手で列挙し直さずに固定する (delete(m, "color") のような
// 変異が入るとここで落ちる)。
func TestProxyRoleIconURLs_PreservesRoleFields(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", []byte("s"), false, true))
	t.Cleanup(func() { entity.SetMediaURLContext(nil) })

	color := "#fff"
	r := &model.Role{
		ID: "r1", Name: "n", Description: "d", Color: &color,
		Target: model.RoleTarget("manual"), IsPublic: true, AsBadge: true,
		DisplayOrder: 3, CondFormula: datatypes.JSON("{}"), Policies: datatypes.JSON("{}"),
	}

	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var wantKeys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wantKeys))

	out := proxyRoleIconURLs([]*model.Role{r})
	require.Len(t, out, 1)
	for k := range wantKeys {
		_, ok := out[0][k]
		assert.True(t, ok, "round-trip でキー %q が失われている", k)
	}
	assert.Len(t, out[0], len(wantKeys), "round-trip でキーが増減している")
}

// UseNumber で decode しているので、int64 の精度を float64 経由で落とさない
// (#3130 review 3周目: policies / condFormula の 2^53 超の整数が
// silent に丸められていた)。
func TestProxyRoleIconURLs_PreservesLargeIntegerPrecision(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", []byte("s"), false, true))
	t.Cleanup(func() { entity.SetMediaURLContext(nil) })

	r := &model.Role{
		ID: "r1", Name: "n",
		Policies: datatypes.JSON(`{"limit":9007334740993}`),
	}
	out := proxyRoleIconURLs([]*model.Role{r})
	require.Len(t, out, 1)
	policies, ok := out[0]["policies"].(map[string]any)
	require.True(t, ok)
	num, ok := policies["limit"].(json.Number)
	require.True(t, ok, "policies の数値は json.Number で保持されるべき")
	assert.Equal(t, "9007334740993", num.String())
}

// round-trip に失敗した role は nil (=応答配列の null 要素) のまま残さず、
// id/name/color/iconUrl だけの最小限フォールバックへ倒す (#3130 review 3周目)。
// **`datatypes.JSON` は中身を検証せず生バイト列をそのまま返す** ので、DB から
// 来る経路は無いが不正な JSON を積めば `json.Marshal` を意図的に失敗させられる。
func TestProxyRoleIconURLs_FallsBackWhenMarshalFails(t *testing.T) {
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		"https://mk.example", "https://mk.example/proxy", []byte("s"), false, true))
	t.Cleanup(func() { entity.SetMediaURLContext(nil) })

	remote := "https://remote.example/r.png"
	color := "#abc"
	broken := &model.Role{
		ID: "broken", Name: "broken-role", Color: &color, IconURL: &remote,
		Policies: datatypes.JSON("not json"),
	}

	out := proxyRoleIconURLs([]*model.Role{broken})
	require.Len(t, out, 1)
	require.NotNil(t, out[0], "round-trip 失敗時も null 要素を残さない")
	assert.Equal(t, "broken", out[0]["id"])
	assert.Equal(t, "broken-role", out[0]["name"])
	assert.Equal(t, &color, out[0]["color"])
	iconURL, _ := out[0]["iconUrl"].(*string)
	require.NotNil(t, iconURL)
	assert.True(t, strings.HasPrefix(*iconURL, "https://mk.example/proxy/image.webp?"),
		"フォールバック時も iconUrl は proxy 済みであるべき, got %q", *iconURL)
}
